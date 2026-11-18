// Package binance decodes Binance spot websocket market data into the
// normalized domain model.
//
// Decoding is pure: it takes a raw frame and returns a domain.Event, so it can
// be covered by table-driven tests using recorded frames without any network
// access. Trade prints, BBO updates, top-N depth snapshots and klines are
// decoded; incremental depth deltas are intentionally ignored.
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
	// Symbol is the fallback symbol for frames that do not carry one. Depth
	// snapshot frames are symbol-less by design, so it is resolved from the
	// stream name first and from this field otherwise.
	Symbol string
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
	case strings.Contains(stream, "@depth"):
		return d.decodeDepth(payload, stream, now)
	case strings.Contains(stream, "@kline_"):
		return d.decodeKline(payload, stream, now)
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

	// A partial-book depth snapshot carries lastUpdateId and no event type.
	if _, ok := probe["lastUpdateId"]; ok {
		return d.decodeDepth(payload, stream, now)
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
			TS:           ts,
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

// depthFrame is a partial book depth snapshot (`<symbol>@depth<N>`). It carries
// no symbol and no event time, so both are filled in from the stream name and
// the local clock.
type depthFrame struct {
	LastUpdateID uint64      `json:"lastUpdateId"`
	Bids         [][2]string `json:"bids"`
	Asks         [][2]string `json:"asks"`
}

func (d Decoder) decodeDepth(payload []byte, stream string, now time.Time) (domain.Event, bool, error) {
	var f depthFrame
	if err := json.Unmarshal(payload, &f); err != nil {
		return domain.Event{}, false, fmt.Errorf("binance: decode depth: %w", err)
	}

	symbol := symbolFromStream(stream)
	if symbol == "" {
		symbol = d.Symbol
	}
	if symbol == "" {
		return domain.Event{}, false, fmt.Errorf("binance: depth frame without symbol")
	}

	bids, err := d.levels("bid", f.Bids)
	if err != nil {
		return domain.Event{}, false, err
	}
	asks, err := d.levels("ask", f.Asks)
	if err != nil {
		return domain.Event{}, false, err
	}

	recvMS := now.UnixMilli()
	return domain.Event{
		Kind:     domain.KindDepth,
		Exchange: Exchange,
		Symbol:   symbol,
		Seq:      f.LastUpdateID,
		TS:       recvMS,
		RecvTS:   recvMS,
		Depth: &domain.Depth{
			UpdateID: f.LastUpdateID,
			Bids:     bids,
			Asks:     asks,
		},
	}, true, nil
}

func (d Decoder) levels(side string, raw [][2]string) ([]domain.Level, error) {
	out := make([]domain.Level, 0, len(raw))
	for _, lv := range raw {
		price, err := d.scaled(side+" price", lv[0], d.PriceScale)
		if err != nil {
			return nil, err
		}
		qty, err := d.scaled(side+" quantity", lv[1], d.QtyScale)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.Level{Price: price, Qty: qty})
	}
	return out, nil
}

// klineFrame is a kline payload.
//
// Every key is declared explicitly. Binance mixes cases inside this object
// ("L" last trade id vs "l" low, "V" taker-buy volume vs "v" volume), and
// encoding/json falls back to a case-insensitive match for undeclared keys,
// which silently decodes the wrong field.
type klineFrame struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
	Kline     struct {
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
}

func (d Decoder) decodeKline(payload []byte, stream string, now time.Time) (domain.Event, bool, error) {
	var f klineFrame
	if err := json.Unmarshal(payload, &f); err != nil {
		return domain.Event{}, false, fmt.Errorf("binance: decode kline: %w", err)
	}

	symbol := f.Kline.Symbol
	if symbol == "" {
		symbol = f.Symbol
	}
	if symbol == "" {
		symbol = symbolFromStream(stream)
	}
	if symbol == "" {
		symbol = d.Symbol
	}
	if symbol == "" {
		return domain.Event{}, false, fmt.Errorf("binance: kline frame without symbol")
	}
	if f.Kline.Interval == "" {
		return domain.Event{}, false, fmt.Errorf("binance: kline frame without interval")
	}

	open, err := d.scaled("kline open", f.Kline.Open, d.PriceScale)
	if err != nil {
		return domain.Event{}, false, err
	}
	high, err := d.scaled("kline high", f.Kline.High, d.PriceScale)
	if err != nil {
		return domain.Event{}, false, err
	}
	low, err := d.scaled("kline low", f.Kline.Low, d.PriceScale)
	if err != nil {
		return domain.Event{}, false, err
	}
	closePrice, err := d.scaled("kline close", f.Kline.Close, d.PriceScale)
	if err != nil {
		return domain.Event{}, false, err
	}
	volume, err := d.scaled("kline volume", f.Kline.Volume, d.QtyScale)
	if err != nil {
		return domain.Event{}, false, err
	}

	recvMS := now.UnixMilli()
	ts := f.EventTime
	if ts <= 0 {
		ts = recvMS
	}

	return domain.Event{
		Kind:     domain.KindKline,
		Exchange: Exchange,
		Symbol:   symbol,
		Seq:      uint64(f.Kline.OpenTime),
		TS:       ts,
		RecvTS:   recvMS,
		Kline: &domain.Kline{
			Interval: f.Kline.Interval,
			OpenTime: f.Kline.OpenTime,
			Open:     open,
			High:     high,
			Low:      low,
			Close:    closePrice,
			Volume:   volume,
			Closed:   f.Kline.Closed,
		},
	}, true, nil
}

// symbolFromStream extracts the symbol from a stream name such as
// "btcusdt@depth20@100ms" and returns it upper-cased.
func symbolFromStream(stream string) string {
	i := strings.IndexByte(stream, '@')
	if i <= 0 {
		return ""
	}
	return strings.ToUpper(stream[:i])
}
