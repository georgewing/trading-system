// Package market defines normalized real-time market data and the in-memory
// store that serves REST snapshots and WebSocket subscriptions.
//
// Prices and quantities are carried as decimal strings exactly as the venue
// published them. Converting them to float would lose precision, and this
// project never rounds market data on the way in.
package market

// Side is the aggressor side of a trade print.
const (
	SideBuy  = "BUY"
	SideSell = "SELL"
)

// Trade is a normalized public trade print.
type Trade struct {
	ID     uint64 `json:"id"`
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
	Qty    string `json:"qty"`
	Side   string `json:"side"` // aggression side: BUY means the taker bought
	Ts     int64  `json:"ts"`   // unix milliseconds
}

// Depth is a top-N order book snapshot. Each level is [price, qty].
type Depth struct {
	Symbol       string      `json:"symbol"`
	LastUpdateID int64       `json:"last_update_id"`
	Bids         [][2]string `json:"bids"`
	Asks         [][2]string `json:"asks"`
	Ts           int64       `json:"ts"`
}

// Kline is one OHLCV candle for a symbol and interval.
type Kline struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	OpenTime int64  `json:"open_time"` // unix milliseconds
	Open     string `json:"open"`
	High     string `json:"high"`
	Low      string `json:"low"`
	Close    string `json:"close"`
	Volume   string `json:"volume"`
	Closed   bool   `json:"closed"`
	Ts       int64  `json:"ts"`
}

// Event is one broadcast unit pushed to WebSocket subscribers. Exactly one of
// the payload pointers is non-nil; Type names which one.
type Event struct {
	Type   string
	Symbol string
	Depth  *Depth
	Trade  *Trade
	Kline  *Kline
}

// Event type names, also used as the WebSocket wire `type` field prefix.
const (
	EventDepth = "depth"
	EventTrade = "trade"
	EventKline = "kline"
)
