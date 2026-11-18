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
	// scope for the current phase; only the top of book is ingested.
	KindTick
)

// String returns the stable topic segment used by subscriptions and metrics.
func (k Kind) String() string {
	switch k {
	case KindTrade:
		return "trade"
	case KindTick:
		return "tick"
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
}

// Topic returns the subscription key for the event, shaped as "kind:symbol"
// (for example "trade:BTCUSDT").
func (e Event) Topic() string {
	return e.Kind.String() + ":" + e.Symbol
}

// Trade is a single executed print.
type Trade struct {
	ID           uint64 // exchange trade id
	Price        int64
	Qty          int64
	BuyerIsMaker bool // true when the buyer was the resting side
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
