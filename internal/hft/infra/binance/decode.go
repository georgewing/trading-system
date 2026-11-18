// Package binance decodes Binance spot websocket market data into the
// normalized domain model.
//
// Decoding is pure: it takes a raw frame and returns a domain.Event, so it can
// be covered by table-driven tests using recorded frames without any network
// access. Only trade prints and best-bid-offer updates are ingested in the
// current phase; depth deltas are intentionally ignored.
package binance

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"trading-system/internal/engine"
	"trading-system/internal/hft/domain"
)

// Exchange is the value stamped on every decoded event.
const Exchange = "binance"

// Decoder converts Binance frames into normalized events.
//
// Scales are the minimal-unit multipliers for the instrument, for example
// 100000000 for eight decimals. Prices and quantities are parsed as decimal
// strings without any floating point round trip; excess precision is rejected
// rather than rounded.
type Decoder struct {
	PriceScale int64
	QtyScale   int64
	// Now supplies receive timestamps; nil means time.Now.
	Now func() time.Time
}

// Decode converts one websocket frame.
//
// ok is false for frames that carry no market data (subscription acks, pongs,
// depth updates) and must be ignored by the caller.
func (d Decoder) Decode(raw []byte) (ev domain.Event, ok bool, err error) {
	now := d.now()

	payload := raw
	stream := ""

	var env envelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Stream != "" && len(env.Data) > 0 {
		stream = env.Stream
		payload = env.Data
	}

	switch {
	case strings.HasSuffix(stream, "@trade"):
		return d.decodeTrade(payload, now)
	case strings.HasSuffix(stream, "@bookTicker"):
		return d.decodeBookTicker(payload, now)
	}

	// No stream name (single-stream socket): sniff the payload instead.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(payload, &probe); err != nil {
		return domain.Event{}, false, fmt.Errorf("binance: decode frame: %w", err)
	}

	if kind, ok := probe["e"]; ok {
		var name string
		_ = json.Unmarshal(kind, &name)
		switch name {
		case "trade":
			return d.decodeTrade(payload, now)
		case "depthUpdate":
			// Not subscribed in this phase; ignore instead of counting an error.
			return domain.Event{}, false, nil
		}
	}

	// BBO frames carry `b`/`a` as decimal strings.
	if bid, ok := probe["b"]; ok && len(bid) > 0 && bid[0] == '"' {
		return d.decodeBookTicker(payload, now)
	}

	return domain.Event{}, false, nil
}

type envelope struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

type tradeFrame struct {
	Symbol  string `json:"s"`
	ID      uint64 `json:"t"`
	Price   string `json:"p"`
	Qty     string `json:"q"`
	TimeMS  int64  `json:"T"`
	IsMaker bool   `json:"m"`
}

type bookTickerFrame struct {
	UpdateID uint64 `json:"u"`
	Symbol   string `json:"s"`
	BidPrice string `json:"b"`
	BidQty   string `json:"B"`
	AskPrice string `json:"a"`
	AskQty   string `json:"A"`
}

func (d Decoder) decodeTrade(payload []byte, now time.Time) (domain.Event, bool, error) {
	var f tradeFrame
	if err := json.Unmarshal(payload, &f); err != nil {
		return domain.Event{}, false, fmt.Errorf("binance: decode trade: %w", err)
	}
	if f.Symbol == "" {
		return domain.Event{}, false, fmt.Errorf("binance: trade frame without symbol")
	}

	price, err := d.scaled("price", f.Price, d.PriceScale)
	if err != nil {
		return domain.Event{}, false, err
	}
	qty, err := d.scaled("quantity", f.Qty, d.QtyScale)
	if err != nil {
		return domain.Event{}, false, err
	}

	recvMS := now.UnixMilli()
	ts := f.TimeMS
	if ts <= 0 {
		ts = recvMS
	}

	return domain.Event{
		Kind:     domain.KindTrade,
		Exchange: Exchange,
		Symbol:   f.Symbol,
		Seq:      f.ID,
		TS:       ts,
		RecvTS:   recvMS,
		Trade: &domain.Trade{
			ID:           f.ID,
			Price:        price,
			Qty:          qty,
			BuyerIsMaker: f.IsMaker,
		},
	}, true, nil
}

func (d Decoder) decodeBookTicker(payload []byte, now time.Time) (domain.Event, bool, error) {
	var f bookTickerFrame
	if err := json.Unmarshal(payload, &f); err != nil {
		return domain.Event{}, false, fmt.Errorf("binance: decode book ticker: %w", err)
	}
	if f.Symbol == "" {
		return domain.Event{}, false, fmt.Errorf("binance: book ticker frame without symbol")
	}

	bidPrice, err := d.scaled("bid price", f.BidPrice, d.PriceScale)
	if err != nil {
		return domain.Event{}, false, err
	}
	bidQty, err := d.scaled("bid quantity", f.BidQty, d.QtyScale)
	if err != nil {
		return domain.Event{}, false, err
	}
	askPrice, err := d.scaled("ask price", f.AskPrice, d.PriceScale)
	if err != nil {
		return domain.Event{}, false, err
	}
	askQty, err := d.scaled("ask quantity", f.AskQty, d.QtyScale)
	if err != nil {
		return domain.Event{}, false, err
	}

	// Binance publishes no event time for BBO, so the receive time is the only
	// honest timestamp. Consumers must not derive ingest latency from ticks.
	recvMS := now.UnixMilli()

	return domain.Event{
		Kind:     domain.KindTick,
		Exchange: Exchange,
		Symbol:   f.Symbol,
		Seq:      f.UpdateID,
		TS:       recvMS,
		RecvTS:   recvMS,
		Tick: &domain.Tick{
			UpdateID: f.UpdateID,
			BidPrice: bidPrice,
			BidQty:   bidQty,
			AskPrice: askPrice,
			AskQty:   askQty,
		},
	}, true, nil
}

// scaled parses a decimal string into minimal units. It reuses the engine
// parser so order quantities and market data share exactly one decimal
// implementation and the same "reject, never round" policy.
func (d Decoder) scaled(field, raw string, scale int64) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("binance: empty %s", field)
	}
	if scale <= 0 {
		return 0, fmt.Errorf("binance: invalid %s scale %d", field, scale)
	}
	v, err := engine.ParseQuantity(raw, scale)
	if err != nil {
		return 0, fmt.Errorf("binance: %s %q: %w", field, raw, err)
	}
	return v, nil
}

func (d Decoder) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}
