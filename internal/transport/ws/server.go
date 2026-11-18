package ws

// WebSocket transport for the trading terminal.
//
// The server is an adapter: it consumes two ports (domain.Fanout for the live
// stream and domain.Snapshotter for stateful topics) and renders the result as
// JSON. It knows nothing about Binance and nothing about matching.
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

	"trading-system/internal/hft/domain"
	"trading-system/internal/hft/engine"
	"trading-system/internal/hft/infra/orders"
	"trading-system/internal/hft/risk"
	"trading-system/internal/observability"
	"trading-system/internal/transport/wire"
)

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
	TypeRiskEvent     = "risk.event"
	TypeRiskBlocked   = "risk.blocked"
	TypeRiskSnapshot  = "risk.snapshot"
)

const (
	topicOrder = "order"
	topicRisk  = "risk"
	// subscribeAll keeps a single fanout subscription per connection; the
	// per-client topic set filters what is actually written out, which is what
	// makes dynamic sub/unsub possible without re-subscribing the fanout.
	subscribeAll = "*"
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
		SendBuffer:     1024,
		ControlBuffer:  64,
		WriteTimeout:   5 * time.Second,
		PingInterval:   30 * time.Second,
		ReadLimit:      1 << 20,
		TradeSnapshot:  50,
		KlineSnapshot:  300,
		MaxTopics:      32,
		OriginPatterns: []string{"*"},
	}
}

// Server serves the /ws endpoint.
type Server struct {
	fanout domain.Fanout
	snap   domain.Snapshotter
	exec   ExecFeed
	risk   RiskFeed
	cfg    ServerConfig
	met    observability.Recorder
}

// NewServer builds a WS server. Nil ports are tolerated: those topics simply
// produce no data.
func NewServer(fanout domain.Fanout, snap domain.Snapshotter, exec ExecFeed, cfg ServerConfig) *Server {
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
		fanout: fanout,
		snap:   snap,
		exec:   exec,
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

// SetRiskFeed installs the optional risk-event feed.
func (s *Server) SetRiskFeed(feed RiskFeed) {
	s.risk = feed
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
		risk:   s.risk,
	}

	marketCh := make(<-chan domain.Event)
	cancelMarket := func() {}
	if s.fanout != nil {
		ch, stop, err := s.fanout.Subscribe([]string{subscribeAll})
		if err != nil {
			log.Printf("ws: subscribe fanout: %v", err)
		} else {
			marketCh = ch
			cancelMarket = stop
		}
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

	riskCh := make(<-chan risk.Event)
	cancelRisk := func() {}
	if s.risk != nil {
		_, ch, stop := s.risk.Subscribe()
		riskCh = ch
		cancelRisk = stop
	}
	defer cancelRisk()

	go func() {
		if err := c.readLoop(ctx); err != nil && ctx.Err() == nil {
			log.Printf("ws: client read ended: %v", err)
		}
		cancel()
	}()

	c.writeLoop(ctx, s.snap, marketCh, execCh, riskCh)
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

// wire DTOs. Prices and quantities are decimal strings.

type depthDTO struct {
	Symbol       string      `json:"symbol"`
	LastUpdateID uint64      `json:"last_update_id"`
	Bids         [][2]string `json:"bids"`
	Asks         [][2]string `json:"asks"`
	Ts           int64       `json:"ts"`
}

type marketTradeDTO struct {
	ID     uint64 `json:"id"`
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
	Qty    string `json:"qty"`
	Side   string `json:"side"`
	Ts     int64  `json:"ts"`
}

type klineDTO struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	OpenTime int64  `json:"open_time"`
	Open     string `json:"open"`
	High     string `json:"high"`
	Low      string `json:"low"`
	Close    string `json:"close"`
	Volume   string `json:"volume"`
	Closed   bool   `json:"closed"`
	Ts       int64  `json:"ts"`
}

type tradeSnapshot struct {
	Symbol string           `json:"symbol"`
	Trades []marketTradeDTO `json:"trades"`
}

type klineSnapshot struct {
	Symbol   string     `json:"symbol"`
	Interval string     `json:"interval"`
	Klines   []klineDTO `json:"klines"`
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
	risk   RiskFeed

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

func (c *client) writeLoop(
	ctx context.Context,
	snap domain.Snapshotter,
	marketCh <-chan domain.Event,
	execCh <-chan engine.ExecReport,
	riskCh <-chan risk.Event,
) {
	ping := time.NewTicker(c.cfg.PingInterval)
	defer ping.Stop()

	if err := c.send(helloPayload{
		ServerTime:        time.Now().UnixMilli(),
		Symbol:            c.cfg.Symbol,
		PricePrecision:    c.cfg.PricePrecision,
		QuantityPrecision: c.cfg.QuantityPrecision,
		Topics: []string{
			"depth:<symbol>", "trade:<symbol>", "kline:<symbol>:<interval>", topicOrder, topicRisk,
		},
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
			c.handleControl(snap, msg)

		case ev, ok := <-marketCh:
			if !ok {
				marketCh = nil
				continue
			}
			if !c.wants(ev.Topic()) {
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

		case ev, ok := <-riskCh:
			if !ok {
				riskCh = nil
				continue
			}
			if !c.wants(topicRisk) {
				continue
			}
			c.send(ev, riskEventType(ev), ev.Symbol)

		case <-ping.C:
			if err := c.ping(ctx); err != nil {
				return
			}
		}
	}
}

func (c *client) handleControl(snap domain.Snapshotter, msg controlMsg) {
	switch msg.Op {
	case "sub":
		c.subscribe(snap, msg)
	case "unsub":
		for _, t := range msg.Topics {
			delete(c.topics, t)
		}
	case "ping":
		c.send(map[string]int64{"ts": msg.Ts}, TypePong, "")
	default:
		c.send(errorPayload{
			Code:    "bad_op",
			Message: fmt.Sprintf("unknown op %q", msg.Op),
		}, TypeError, "")
	}
}

func (c *client) subscribe(snap domain.Snapshotter, msg controlMsg) {
	for _, t := range msg.Topics {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len(c.topics) >= c.cfg.MaxTopics {
			c.send(errorPayload{
				Code:    "too_many_topics",
				Message: "subscription limit reached",
			}, TypeError, "")
			return
		}
		c.topics[t] = struct{}{}
		c.sendSnapshot(snap, t)
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

func (c *client) sendSnapshot(snap domain.Snapshotter, topic string) {
	parts := strings.Split(topic, ":")
	if parts[0] == topicRisk {
		if c.risk != nil {
			if events := c.risk.Recent(50); len(events) > 0 {
				c.send(events, TypeRiskSnapshot, "")
			}
		}
		return
	}
	if snap == nil {
		return
	}
	switch parts[0] {
	case "depth":
		if len(parts) != 2 {
			return
		}
		if d, ok := snap.Depth(parts[1]); ok {
			c.send(c.depthDTO(d, parts[1]), TypeDepth, parts[1])
		}

	case "trade":
		if len(parts) != 2 {
			return
		}
		trades := snap.Trades(parts[1], c.cfg.TradeSnapshot)
		if len(trades) > 0 {
			dto := tradeSnapshot{Symbol: parts[1], Trades: make([]marketTradeDTO, 0, len(trades))}
			for _, t := range trades {
				dto.Trades = append(dto.Trades, c.tradeDTO(t, parts[1]))
			}
			c.send(dto, TypeTradeSnapshot, parts[1])
		}

	case "kline":
		if len(parts) != 3 {
			return
		}
		klines := snap.Klines(parts[1], parts[2], c.cfg.KlineSnapshot)
		if len(klines) > 0 {
			dto := klineSnapshot{
				Symbol:   parts[1],
				Interval: parts[2],
				Klines:   make([]klineDTO, 0, len(klines)),
			}
			for _, k := range klines {
				dto.Klines = append(dto.Klines, c.klineDTO(k, parts[1]))
			}
			c.send(dto, TypeKlineSnapshot, parts[1])
		}
	}
}

func (c *client) sendMarketEvent(ev domain.Event) {
	switch ev.Kind {
	case domain.KindDepth:
		if ev.Depth != nil {
			c.send(c.depthDTO(*ev.Depth, ev.Symbol), TypeDepth, ev.Symbol)
		}
	case domain.KindTrade:
		if ev.Trade != nil {
			c.send(c.tradeDTO(*ev.Trade, ev.Symbol), TypeTrade, ev.Symbol)
		}
	case domain.KindKline:
		if ev.Kline != nil {
			c.send(c.klineDTO(*ev.Kline, ev.Symbol), TypeKline, ev.Symbol)
		}
	}
}

func (c *client) depthDTO(d domain.Depth, symbol string) depthDTO {
	bids := make([][2]string, 0, len(d.Bids))
	for _, lv := range d.Bids {
		bids = append(bids, wire.Level(lv.Price, lv.Qty, c.cfg.PricePrecision, c.cfg.QuantityPrecision))
	}
	asks := make([][2]string, 0, len(d.Asks))
	for _, lv := range d.Asks {
		asks = append(asks, wire.Level(lv.Price, lv.Qty, c.cfg.PricePrecision, c.cfg.QuantityPrecision))
	}
	return depthDTO{
		Symbol:       symbol,
		LastUpdateID: d.UpdateID,
		Bids:         bids,
		Asks:         asks,
		Ts:           time.Now().UnixMilli(),
	}
}

func (c *client) tradeDTO(t domain.Trade, symbol string) marketTradeDTO {
	side := "BUY"
	if t.BuyerIsMaker {
		// The buyer was resting, so the taker sold into the bid.
		side = "SELL"
	}
	return marketTradeDTO{
		ID:     t.ID,
		Symbol: symbol,
		Price:  wire.Decimal(t.Price, c.cfg.PricePrecision),
		Qty:    wire.Decimal(t.Qty, c.cfg.QuantityPrecision),
		Side:   side,
		Ts:     t.TS,
	}
}

func (c *client) klineDTO(k domain.Kline, symbol string) klineDTO {
	return klineDTO{
		Symbol:   symbol,
		Interval: k.Interval,
		OpenTime: k.OpenTime,
		Open:     wire.Decimal(k.Open, c.cfg.PricePrecision),
		High:     wire.Decimal(k.High, c.cfg.PricePrecision),
		Low:      wire.Decimal(k.Low, c.cfg.PricePrecision),
		Close:    wire.Decimal(k.Close, c.cfg.PricePrecision),
		Volume:   wire.Decimal(k.Volume, c.cfg.QuantityPrecision),
		Closed:   k.Closed,
		Ts:       k.OpenTime,
	}
}

func (c *client) wants(topic string) bool {
	_, ok := c.topics[topic]
	return ok
}

// riskEventType maps an action to the wire type. Blocking actions use
// risk.blocked, state recoveries use risk.event.
func riskEventType(ev risk.Event) string {
	switch ev.Action {
	case risk.ActionReject, risk.ActionCancelOnly, risk.ActionPause, risk.ActionHalt:
		return TypeRiskBlocked
	default:
		return TypeRiskEvent
	}
}

// send writes one envelope. A write failure is recorded and surfaced by the
// caller's loop on the next iteration.
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

// execReportDTO is the wire form of an execution report.
//
// OrderID is a string because a 64-bit snowflake id does not fit in a
// JavaScript number without losing precision.
type execReportDTO struct {
	ExecID        string    `json:"exec_id,omitempty"`
	Seq           uint64    `json:"seq,omitempty"`
	OrderID       string    `json:"order_id"`
	ClientOrderID string    `json:"client_order_id,omitempty"`
	AccountID     uint64    `json:"account_id,omitempty"`
	Symbol        string    `json:"symbol,omitempty"`
	Side          string    `json:"side,omitempty"`
	ExecType      string    `json:"exec_type,omitempty"`
	Status        string    `json:"status"`
	Filled        int64     `json:"filled"`
	Leaves        int64     `json:"leaves"`
	LastPx        int64     `json:"last_px,omitempty"`
	LastQty       int64     `json:"last_qty,omitempty"`
	CumQty        int64     `json:"cum_qty,omitempty"`
	LeavesQty     int64     `json:"leaves_qty,omitempty"`
	Liquidity     string    `json:"liquidity,omitempty"`
	Venue         string    `json:"venue,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	Timestamp     int64     `json:"timestamp"`
	Trade         *tradeDTO `json:"trade,omitempty"`
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
		ExecID:        strconv.FormatUint(rep.ExecID, 10),
		Seq:           rep.Seq,
		OrderID:       strconv.FormatUint(rep.OrderID, 10),
		ClientOrderID: rep.ClientOrderID,
		AccountID:     rep.AccountID,
		Symbol:        rep.Symbol,
		Side:          orders.SideName(rep.Side),
		ExecType:      string(rep.ExecType),
		Status:        orders.StatusName(rep.Status),
		Filled:        rep.Filled,
		Leaves:        rep.Leaves,
		LastPx:        rep.LastPx,
		LastQty:       rep.LastQty,
		CumQty:        rep.CumQty,
		LeavesQty:     rep.LeavesQty,
		Liquidity:     string(rep.Liquidity),
		Venue:         rep.Venue,
		Reason:        rep.Reason,
		Timestamp:     rep.Timestamp,
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
