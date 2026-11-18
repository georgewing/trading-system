// Package binance implements a public market-data feed for Binance spot.
//
// It consumes the combined stream endpoint (depth + trade + kline), normalizes
// payloads into internal/market types and pushes them into a market.Store. It
// never touches the matching pipeline and needs no API key.
package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/coder/websocket"

	"trading-system/internal/market"
)

// Metrics is the narrow observability surface the feed needs.
type Metrics interface {
	IncFeedMessage(stream string)
	IncFeedReconnect(stream string)
	SetFeedLag(stream string, d time.Duration)
}

// Config configures the Binance public feed.
type Config struct {
	WSURL         string        // base websocket URL, e.g. wss://stream.binance.com:9443
	Symbol        string        // exchange symbol, e.g. BTCUSDT
	DepthLevels   int           // 5, 10 or 20
	DepthSpeed    string        // "1000ms" or "100ms"
	KlineInterval string        // e.g. "1m"
	ReconnectMin  time.Duration // first backoff
	ReconnectMax  time.Duration // backoff ceiling
	ReadTimeout   time.Duration // one read may wait this long before we recycle the conn
}

// DefaultConfig returns a config suitable for BTCUSDT on Binance spot.
func DefaultConfig() Config {
	return Config{
		WSURL:         "wss://stream.binance.com:9443",
		Symbol:        "BTCUSDT",
		DepthLevels:   20,
		DepthSpeed:    "100ms",
		KlineInterval: "1m",
		ReconnectMin:  time.Second,
		ReconnectMax:  30 * time.Second,
		ReadTimeout:   90 * time.Second,
	}
}

// Client is a reconnecting public market-data feed.
type Client struct {
	cfg     Config
	out     *market.Store
	met     Metrics
	rnd     *rand.Rand
	url     string
	streams map[string]string // stream name -> internal event type
}

// NewClient validates cfg and builds a feed writing into store. A nil metrics
// recorder disables metric reporting.
func NewClient(cfg Config, store *market.Store, met Metrics) (*Client, error) {
	def := DefaultConfig()
	if cfg.WSURL == "" {
		cfg.WSURL = def.WSURL
	}
	if cfg.Symbol == "" {
		return nil, errors.New("binance: symbol is required")
	}
	if store == nil {
		return nil, errors.New("binance: store is required")
	}
	switch cfg.DepthLevels {
	case 0:
		cfg.DepthLevels = def.DepthLevels
	case 5, 10, 20:
	default:
		return nil, fmt.Errorf("binance: unsupported depth levels %d (want 5, 10 or 20)", cfg.DepthLevels)
	}
	if cfg.DepthSpeed == "" {
		cfg.DepthSpeed = def.DepthSpeed
	}
	if cfg.DepthSpeed != "100ms" && cfg.DepthSpeed != "1000ms" {
		return nil, fmt.Errorf("binance: unsupported depth speed %q", cfg.DepthSpeed)
	}
	if cfg.KlineInterval == "" {
		cfg.KlineInterval = def.KlineInterval
	}
	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = def.ReconnectMin
	}
	if cfg.ReconnectMax < cfg.ReconnectMin {
		cfg.ReconnectMax = def.ReconnectMax
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = def.ReadTimeout
	}

	lower := strings.ToLower(cfg.Symbol)
	depthStream := fmt.Sprintf("%s@depth%d@%s", lower, cfg.DepthLevels, cfg.DepthSpeed)
	tradeStream := lower + "@trade"
	klineStream := fmt.Sprintf("%s@kline_%s", lower, cfg.KlineInterval)

	return &Client{
		cfg: cfg,
		out: store,
		met: met,
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
		url: fmt.Sprintf("%s/stream?streams=%s/%s/%s",
			strings.TrimSuffix(cfg.WSURL, "/"), depthStream, tradeStream, klineStream),
		streams: map[string]string{
			depthStream: market.EventDepth,
			tradeStream: market.EventTrade,
			klineStream: market.EventKline,
		},
	}, nil
}

// URL returns the combined stream URL. Useful for logs and tests.
func (c *Client) URL() string { return c.url }

// Run connects and reads until ctx is cancelled, reconnecting with exponential
// backoff and jitter. It only returns a non-nil error when ctx ends.
func (c *Client) Run(ctx context.Context) error {
	backoff := c.cfg.ReconnectMin

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		received, err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			log.Printf("binance: stream error: %v", err)
		}

		if received {
			backoff = c.cfg.ReconnectMin
		}
		if c.met != nil {
			c.met.IncFeedReconnect("binance")
		}

		wait := jitter(backoff, c.rnd)
		log.Printf("binance: reconnecting in %s", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		backoff *= 2
		if backoff > c.cfg.ReconnectMax {
			backoff = c.cfg.ReconnectMax
		}
	}
}

// runOnce opens one connection and reads messages until it fails. It reports
// whether at least one message was decoded, so the caller can reset backoff
// after a healthy session.
func (c *Client) runOnce(ctx context.Context) (bool, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, c.url, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(8 << 20)

	received := false
	for {
		readCtx, readCancel := context.WithTimeout(ctx, c.cfg.ReadTimeout)
		_, data, err := conn.Read(readCtx)
		readCancel()

		if err != nil {
			if ctx.Err() != nil {
				return received, ctx.Err()
			}
			return received, fmt.Errorf("read: %w", err)
		}

		if err := c.handle(data); err != nil {
			// A single malformed frame must not kill the feed.
			log.Printf("binance: decode error: %v", err)
			continue
		}
		received = true
	}
}

// combined is the envelope used by the /stream endpoint.
type combined struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

func (c *Client) handle(raw []byte) error {
	var env combined
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}

	kind, ok := c.streams[env.Stream]
	if !ok {
		return nil // unrelated stream on a shared connection
	}

	switch kind {
	case market.EventDepth:
		return c.handleDepth(env.Data)
	case market.EventTrade:
		return c.handleTrade(env.Data)
	case market.EventKline:
		return c.handleKline(env.Data)
	default:
		return nil
	}
}

type depthPayload struct {
	LastUpdateID int64       `json:"lastUpdateId"`
	Bids         [][2]string `json:"bids"`
	Asks         [][2]string `json:"asks"`
}

func (c *Client) handleDepth(raw json.RawMessage) error {
	var p depthPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}

	c.out.ApplyDepth(market.Depth{
		Symbol:       c.cfg.Symbol,
		LastUpdateID: p.LastUpdateID,
		Bids:         p.Bids,
		Asks:         p.Asks,
		Ts:           time.Now().UnixMilli(),
	})
	c.incMessage(market.EventDepth)
	return nil
}

type tradePayload struct {
	// EventType pins the lowercase "e" key. Without it, encoding/json falls back
	// to a case-insensitive match and tries to decode "trade" into EventTime.
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	ID        uint64 `json:"t"`
	Symbol    string `json:"s"`
	Price     string `json:"p"`
	Qty       string `json:"q"`
	TradeTime int64  `json:"T"`
	IsMaker   bool   `json:"m"` // true when the buyer is the maker
}

func (c *Client) handleTrade(raw json.RawMessage) error {
	var p tradePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}

	// Buyer is the maker, so the taker sold into the bid.
	side := market.SideBuy
	if p.IsMaker {
		side = market.SideSell
	}

	symbol := p.Symbol
	if symbol == "" {
		symbol = c.cfg.Symbol
	}

	c.out.ApplyTrade(market.Trade{
		ID:     p.ID,
		Symbol: symbol,
		Price:  p.Price,
		Qty:    p.Qty,
		Side:   side,
		Ts:     p.TradeTime,
	})
	c.incMessage(market.EventTrade)
	c.observeLag(market.EventTrade, p.EventTime)
	return nil
}

type klinePayload struct {
	// Every key is declared so encoding/json never falls back to a
	// case-insensitive match. Binance mixes cases in this object ("L" last trade
	// id vs "l" low, "V" taker volume vs "v" volume), and the fallback silently
	// decodes the wrong value when a field is missing.
	Kline struct {
		OpenTime      int64  `json:"t"`
		CloseTime     int64  `json:"T"`
		Symbol        string `json:"s"`
		Interval      string `json:"i"`
		FirstTradeID  int64  `json:"f"`
		LastTradeID   int64  `json:"L"`
		Open          string `json:"o"`
		Close         string `json:"c"`
		High          string `json:"h"`
		Low           string `json:"l"`
		Volume        string `json:"v"`
		Trades        int64  `json:"n"`
		Closed        bool   `json:"x"`
		QuoteVolume   string `json:"q"`
		TakerBuyBase  string `json:"V"`
		TakerBuyQuote string `json:"Q"`
	} `json:"k"`
	// EventType pins the lowercase "e" key; see tradePayload for why.
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
}

func (c *Client) handleKline(raw json.RawMessage) error {
	var p klinePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}

	k := p.Kline
	symbol := k.Symbol
	if symbol == "" {
		symbol = c.cfg.Symbol
	}
	interval := k.Interval
	if interval == "" {
		interval = c.cfg.KlineInterval
	}

	c.out.ApplyKline(market.Kline{
		Symbol:   symbol,
		Interval: interval,
		OpenTime: k.OpenTime,
		Open:     k.Open,
		High:     k.High,
		Low:      k.Low,
		Close:    k.Close,
		Volume:   k.Volume,
		Closed:   k.Closed,
		Ts:       p.EventTime,
	})
	c.incMessage(market.EventKline)
	c.observeLag(market.EventKline, p.EventTime)
	return nil
}

func (c *Client) incMessage(stream string) {
	if c.met != nil {
		c.met.IncFeedMessage(stream)
	}
}

// observeLag records how stale a venue event is when it reaches us.
func (c *Client) observeLag(stream string, eventMillis int64) {
	if c.met == nil || eventMillis <= 0 {
		return
	}
	lag := time.Since(time.UnixMilli(eventMillis))
	if lag < 0 {
		lag = 0
	}
	c.met.SetFeedLag(stream, lag)
}

// jitter applies +/-20% randomization so reconnecting clients do not stampede.
func jitter(d time.Duration, rnd *rand.Rand) time.Duration {
	if d <= 0 {
		return 0
	}
	delta := float64(d) * 0.2
	return d + time.Duration((rnd.Float64()*2-1)*delta)
}
