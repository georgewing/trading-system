package ws

// WebSocket transport for the trading terminal.
//
// One connection multiplexes market-data topics and execution reports. Every
// frame carries a per-connection monotonic seq so a client can detect gaps; a
// gap means "resubscribe", and stateful topics answer a subscribe with a full
// snapshot instead of a delta.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"trading-system/internal/engine"
	"trading-system/internal/market"
	"trading-system/internal/observability"
	"trading-system/internal/orders"
)

// MarketFeed is the market-data surface the WS server reads from.
type MarketFeed interface {
	Subscribe(buffer int) (<-chan market.Event, func())
	Depth(symbol string) (market.Depth, bool)
	Trades(symbol string, limit int) []market.Trade
	Klines(symbol, interval string, limit int) []market.Kline
}

// ExecFeed is the execution-report surface the WS server reads from.
type ExecFeed interface {
	Subscribe() (uint64, <-chan engine.ExecReport, func())
}

// Wire message types.
const (
	TypeHello         = "system.hello"
	TypePong          = "system.pong"
	TypeError         = "system.error"
	TypeDepth         = "market.depth"
	TypeTrade         = "market.trade"
	TypeTradeSnapshot = "market.trade.snapshot"
	TypeKline         = "market.kline"
	TypeKlineSnapshot = "market.kline.snapshot"
	TypeOrder         = "order.exec"
)

// Topic prefixes accepted from clients.
const (
	topicDepth = "depth"
	topicTrade = "trade"
	topicKline = "kline"
	topicOrder = "order"
)

// ServerConfig configures the /ws endpoint.
type ServerConfig struct {
	Symbol            string
	PricePrecision    int
	QuantityPrecision int

	SendBuffer    int
	ControlBuffer int
	WriteTimeout  time.Duration
	PingInterval  time.Duration
	ReadLimit     int64

	TradeSnapshot int
	KlineSnapshot int
	MaxTopics     int

	// OriginPatterns follows coder/websocket semantics. The default "*" allows
	// any origin, which fits local development behind a Vite proxy.
	OriginPatterns []string
}

// DefaultServerConfig returns sensible terminal defaults.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		SendBuffer:    1024,
		ControlBuffer: 64,
		WriteTimeout:  5 * time.Second,
		PingInterval:  30 * time.Second,
		ReadLimit:     1 << 20,
		TradeSnapshot: 50,
		KlineSnapshot: 300,
		MaxTopics:     32,
		OriginPatterns: []string{
			"*",
		},
	}
}

// Server serves the /ws endpoint.
type Server struct {
	market MarketFeed
	exec   ExecFeed
	cfg    ServerConfig
	met    observability.Recorder
}

// NewServer builds a WS server. A nil market or exec feed is tolerated; those
// topics simply produce no data.
func NewServer(m MarketFeed, e ExecFeed, cfg ServerConfig) *Server {
	def := DefaultServerConfig()
	if cfg.SendBuffer <= 0 {
		cfg.SendBuffer = def.SendBuffer
	}
	if cfg.ControlBuffer <= 0 {
		cfg.ControlBuffer = def.ControlBuffer
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = def.WriteTimeout
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = def.PingInterval
	}
	if cfg.ReadLimit <= 0 {
		cfg.ReadLimit = def.ReadLimit
	}
	if cfg.TradeSnapshot <= 0 {
		cfg.TradeSnapshot = def.TradeSnapshot
	}
	if cfg.KlineSnapshot <= 0 {
		cfg.KlineSnapshot = def.KlineSnapshot
	}
	if cfg.MaxTopics <= 0 {
		cfg.MaxTopics = def.MaxTopics
	}
	if len(cfg.OriginPatterns) == 0 {
		cfg.OriginPatterns = def.OriginPatterns
	}

	return &Server{
		market: m,
		exec:   e,
		cfg:    cfg,
		met:    observability.NoopRecorder(),
	}
}

// SetMetrics injects a recorder; nil falls back to a no-op.
func (s *Server) SetMetrics(rec observability.Recorder) {
	if rec == nil {
		s.met = observability.NoopRecorder()
		return
	}
	s.met = rec
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  s.cfg.OriginPatterns,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(s.cfg.ReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := &client{
		cfg:    s.cfg,
		met:    s.met,
		conn:   conn,
		topics: make(map[string]struct{}, 8),
		ctl:    make(chan controlMsg, s.cfg.ControlBuffer),
	}

	marketCh := make(<-chan market.Event)
	cancelMarket := func() {}
	if s.market != nil {
		ch, stop := s.market.Subscribe(s.cfg.SendBuffer)
		marketCh = ch
		cancelMarket = stop
	}
	defer cancelMarket()

	execCh := make(<-chan engine.ExecReport)
	cancelExec := func() {}
	if s.exec != nil {
		_, ch, stop := s.exec.Subscribe()
		execCh = ch
		cancelExec = stop
	}
	defer cancelExec()

	go func() {
		if err := c.readLoop(ctx); err != nil && ctx.Err() == nil {
			log.Printf("ws: client read ended: %v", err)
		}
		cancel()
	}()

	c.writeLoop(ctx, s.market, marketCh, execCh)
}

// controlMsg is a client request forwarded from the reader to the writer. The
// writer owns subscription state, so no mutex is needed.
type controlMsg struct {
	Op      string   `json:"op"`
	Topics  []string `json:"topics"`
	LastSeq uint64   `json:"last_seq"`
	Ts      int64    `json:"ts"`
}

type envelope struct {
	V      int    `json:"v"`
	Seq    uint64 `json:"seq"`
	Ts     int64  `json:"ts"`
	Type   string `json:"type"`
	Symbol string `json:"symbol,omitempty"`
	Data   any    `json:"data"`
}

type helloPayload struct {
	ServerTime        int64    `json:"server_time"`
	Symbol            string   `json:"symbol"`
	PricePrecision    int      `json:"price_precision"`
	QuantityPrecision int      `json:"quantity_precision"`
	Topics            []string `json:"topics"`
}

type tradeSnapshot struct {
	Symbol string         `json:"symbol"`
	Trades []market.Trade `json:"trades"`
}

type klineSnapshot struct {
	Symbol   string         `json:"symbol"`
	Interval string         `json:"interval"`
	Klines   []market.Kline `json:"klines"`
}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type client struct {
	cfg    ServerConfig
	met    observability.Recorder
	conn   *websocket.Conn
	topics map[string]struct{}
	ctl    chan controlMsg

	seq      uint64
	writeErr error
}

func (c *client) readLoop(ctx context.Context) error {
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return err
		}

		var msg controlMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		select {
		case c.ctl <- msg:
		default:
			// Control flooding: drop instead of blocking the reader.
			c.met.IncWSDrop()
		}
	}
}

func (c *client) writeLoop(ctx context.Context, feed MarketFeed, marketCh <-chan market.Event, execCh <-chan engine.ExecReport) {
	ping := time.NewTicker(c.cfg.PingInterval)
	defer ping.Stop()

	if err := c.send(helloPayload{
		ServerTime:        time.Now().UnixMilli(),
		Symbol:            c.cfg.Symbol,
		PricePrecision:    c.cfg.PricePrecision,
		QuantityPrecision: c.cfg.QuantityPrecision,
		Topics:            []string{"depth:<symbol>", "trade:<symbol>", "kline:<symbol>:<interval>", topicOrder},
	}, TypeHello, c.cfg.Symbol); err != nil {
		return
	}

	for {
		if c.writeErr != nil {
			return
		}

		select {
		case <-ctx.Done():
			return

		case msg := <-c.ctl:
			c.handleControl(ctx, feed, msg)

		case ev, ok := <-marketCh:
			if !ok {
				marketCh = nil
				continue
			}
			if !c.wants(eventTopic(ev)) {
				continue
			}
			c.sendMarketEvent(ev)

		case rep, ok := <-execCh:
			if !ok {
				execCh = nil
				continue
			}
			if !c.wants(topicOrder) {
				continue
			}
			c.send(dtoFromExecReport(rep), TypeOrder, "")

		case <-ping.C:
			if err := c.ping(ctx); err != nil {
				return
			}
		}
	}
}

func (c *client) handleControl(ctx context.Context, feed MarketFeed, msg controlMsg) {
	switch msg.Op {
	case "sub":
		c.subscribe(ctx, feed, msg)
	case "unsub":
		for _, t := range msg.Topics {
			delete(c.topics, t)
		}
	case "ping":
		c.send(map[string]int64{"ts": msg.Ts}, TypePong, "")
	default:
		c.send(errorPayload{Code: "bad_op", Message: fmt.Sprintf("unknown op %q", msg.Op)}, TypeError, "")
	}
}

func (c *client) subscribe(ctx context.Context, feed MarketFeed, msg controlMsg) {
	for _, t := range msg.Topics {
		if len(c.topics) >= c.cfg.MaxTopics {
			c.send(errorPayload{Code: "too_many_topics", Message: "subscription limit reached"}, TypeError, "")
			return
		}
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		c.topics[t] = struct{}{}
		c.sendSnapshot(feed, t)
	}

	// A gap detected by the client is answered by re-sending snapshots, which
	// is exactly what subscribe already does for stateful topics.
	if msg.LastSeq > 0 && msg.LastSeq < c.seq {
		c.send(errorPayload{
			Code:    "resync",
			Message: fmt.Sprintf("last_seq %d is behind server seq %d; snapshots re-sent", msg.LastSeq, c.seq),
		}, TypeError, "")
	}
}

func (c *client) sendSnapshot(feed MarketFeed, topic string) {
	if feed == nil {
		return
	}

	parts := strings.Split(topic, ":")
	switch parts[0] {
	case topicDepth:
		if len(parts) != 2 {
			return
		}
		if d, ok := feed.Depth(parts[1]); ok {
			c.send(d, TypeDepth, d.Symbol)
		}

	case topicTrade:
		if len(parts) != 2 {
			return
		}
		trades := feed.Trades(parts[1], c.cfg.TradeSnapshot)
		if len(trades) > 0 {
			c.send(tradeSnapshot{Symbol: parts[1], Trades: trades}, TypeTradeSnapshot, parts[1])
		}

	case topicKline:
		if len(parts) != 3 {
			return
		}
		klines := feed.Klines(parts[1], parts[2], c.cfg.KlineSnapshot)
		if len(klines) > 0 {
			c.send(klineSnapshot{Symbol: parts[1], Interval: parts[2], Klines: klines},
				TypeKlineSnapshot, parts[1])
		}
	}
}

func (c *client) sendMarketEvent(ev market.Event) {
	switch ev.Type {
	case market.EventDepth:
		if ev.Depth != nil {
			c.send(ev.Depth, TypeDepth, ev.Symbol)
		}
	case market.EventTrade:
		if ev.Trade != nil {
			c.send(ev.Trade, TypeTrade, ev.Symbol)
		}
	case market.EventKline:
		if ev.Kline != nil {
			c.send(ev.Kline, TypeKline, ev.Symbol)
		}
	}
}

func (c *client) wants(topic string) bool {
	_, ok := c.topics[topic]
	return ok
}

// send writes one envelope. A write failure closes the connection by returning
// the error to the caller's loop.
func (c *client) send(data any, msgType, symbol string) error {
	c.seq++

	payload, err := json.Marshal(envelope{
		V:      1,
		Seq:    c.seq,
		Ts:     time.Now().UnixMilli(),
		Type:   msgType,
		Symbol: symbol,
		Data:   data,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.WriteTimeout)
	defer cancel()

	if err := c.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		c.met.IncWSDrop()
		c.writeErr = err
		return err
	}
	return nil
}

func (c *client) ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, c.cfg.WriteTimeout)
	defer cancel()
	return c.conn.Ping(pingCtx)
}

// eventTopic maps a market event to its subscription topic.
func eventTopic(ev market.Event) string {
	switch ev.Type {
	case market.EventDepth:
		return topicDepth + ":" + ev.Symbol
	case market.EventTrade:
		return topicTrade + ":" + ev.Symbol
	case market.EventKline:
		if ev.Kline != nil {
			return topicKline + ":" + ev.Symbol + ":" + ev.Kline.Interval
		}
	}
	return ""
}

// execReportDTO is the wire form of an execution report.
//
// OrderID is a string because a 64-bit snowflake id does not fit in a
// JavaScript number without losing precision.
type execReportDTO struct {
	OrderID   string    `json:"order_id"`
	Status    string    `json:"status"`
	Filled    int64     `json:"filled"`
	Leaves    int64     `json:"leaves"`
	Reason    string    `json:"reason,omitempty"`
	Timestamp int64     `json:"timestamp"`
	Trade     *tradeDTO `json:"trade,omitempty"`
}

type tradeDTO struct {
	ID          uint64 `json:"id"`
	Symbol      string `json:"symbol"`
	Price       int64  `json:"price"`
	Quantity    int64  `json:"quantity"`
	BuyOrderID  uint64 `json:"buy_order_id"`
	SellOrderID uint64 `json:"sell_order_id"`
	Timestamp   int64  `json:"timestamp"`
}

func dtoFromExecReport(rep engine.ExecReport) execReportDTO {
	dto := execReportDTO{
		OrderID:   formatOrderID(rep.OrderID),
		Status:    orders.StatusName(rep.Status),
		Filled:    rep.Filled,
		Leaves:    rep.Leaves,
		Reason:    rep.Reason,
		Timestamp: rep.Timestamp,
	}
	if rep.Trade != nil {
		dto.Trade = &tradeDTO{
			ID:          rep.Trade.ID,
			Symbol:      rep.Trade.Symbol,
			Price:       rep.Trade.Price,
			Quantity:    rep.Trade.Quantity,
			BuyOrderID:  rep.Trade.BuyOrderID,
			SellOrderID: rep.Trade.SellOrderID,
			Timestamp:   rep.Trade.Timestamp,
		}
	}
	return dto
}

func formatOrderID(id uint64) string {
	return strconv.FormatUint(id, 10)
}
