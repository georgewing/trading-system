// Package domain defines the normalized market data model and the ports shared
// by the market data gateway, the storage writers and the distribution layer.
//
// It is deliberately dependency-free: nothing here knows about a specific
// exchange, ClickHouse or websockets. Treat the exported types as a frozen
// contract once the ingest, storage and distribution workstreams start, so they
// can evolve independently without editing this package.
package domain

// Kind identifies the type of market data carried by an Event.
type Kind uint8

const (
	// KindTrade is an executed trade print.
	KindTrade Kind = iota + 1
	// KindTick is a best-bid-offer (BBO) update. Depth increments are out of
	// scope; the gateway ingests full top-N depth snapshots as KindDepth.
	KindTick
	// KindDepth is a full top-N order book snapshot.
	KindDepth
	// KindKline is one OHLCV candle update for a symbol and interval.
	KindKline
)

// String returns the stable topic segment used by subscriptions and metrics.
func (k Kind) String() string {
	switch k {
	case KindTrade:
		return "trade"
	case KindTick:
		return "tick"
	case KindDepth:
		return "depth"
	case KindKline:
		return "kline"
	default:
		return "unknown"
	}
}

// Event is one normalized market data update. Exactly one of Trade or Tick is
// set, matching Kind.
//
// Prices and quantities are always integers in the instrument's minimal unit
// (the precision scale configured for the symbol); floats never cross this
// boundary. Times are Unix milliseconds: TS is the exchange event time and
// RecvTS is the local receive time, so ingest latency is RecvTS-TS.
type Event struct {
	Kind     Kind
	Exchange string
	Symbol   string
	Seq      uint64 // exchange sequence or update id, used for gap detection
	TS       int64
	RecvTS   int64
	Trade    *Trade
	Tick     *Tick
	Depth    *Depth
	Kline    *Kline
}

// Topic returns the subscription key for the event. Trade, tick and depth use
// "kind:symbol"; kline adds the interval so one symbol can have several
// candle streams: "kline:BTCUSDT:1m".
func (e Event) Topic() string {
	if e.Kind == KindKline && e.Kline != nil && e.Kline.Interval != "" {
		return e.Kind.String() + ":" + e.Symbol + ":" + e.Kline.Interval
	}
	return e.Kind.String() + ":" + e.Symbol
}

// Trade is a single executed print.
type Trade struct {
	ID           uint64 // exchange trade id
	Price        int64
	Qty          int64
	BuyerIsMaker bool  // true when the buyer was the resting side
	TS           int64 // exchange trade time, unix milliseconds
}

// Tick is a best-bid-offer update. Exchanges such as Binance publish no event
// time for BBO, so producers must mirror RecvTS into TS for ticks.
type Tick struct {
	UpdateID uint64
	BidPrice int64
	BidQty   int64
	AskPrice int64
	AskQty   int64
}

// Level is one price level of a depth snapshot.
type Level struct {
	Price int64
	Qty   int64
}

// Depth is a full top-N order book snapshot. Bids are ordered best-first
// (descending price) and asks ascending, as published by the venue.
type Depth struct {
	UpdateID uint64
	Bids     []Level
	Asks     []Level
}

// Kline is one OHLCV candle. OpenTime and the prices/volume are in the same
// conventions as the rest of the package: millisecond timestamps and minimal
// units.
type Kline struct {
	Interval string
	OpenTime int64
	Open     int64
	High     int64
	Low      int64
	Close    int64
	Volume   int64
	Closed   bool
}
