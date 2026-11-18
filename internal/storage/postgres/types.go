package postgres

import "time"

// ExecEvent is the persisted form of an execution event. It is defined here
// instead of reusing engine types so persistence does not break when the
// matching engine evolves. A zero TradeID is written as NULL.
type ExecEvent struct {
	ExecID        uint64
	Seq           uint64
	OrderID       uint64
	ClientOrderID string
	AccountID     uint64
	Symbol        string
	Side          int8
	ExecType      string
	Status        int8
	LastPx        int64
	LastQty       int64
	CumQty        int64
	LeavesQty     int64
	Liquidity     string
	TradeID       uint64
	Reason        string
	Venue         string
	ReportedAt    time.Time
}

// Trade is the persisted form of a trade. A zero MakerOrderID is written as
// NULL because snowflake IDs are always positive.
type Trade struct {
	ID           uint64
	Symbol       string
	Price        int64
	Quantity     int64
	BuyOrderID   uint64
	SellOrderID  uint64
	MakerOrderID uint64
	TradedAt     time.Time
}

// Order is the persisted form of an order. Quantity is a decimal string so
// callers keep full precision before PostgreSQL stores it as NUMERIC(38, 18).
type Order struct {
	ID            uint64
	ClientOrderID string
	AccountID     uint64
	Symbol        string
	Side          int8
	OrderType     string
	TimeInForce   string
	Price         int64
	Quantity      string
	Leaves        int64
	Filled        int64
	Status        int8
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Position is the persisted form of one account's position in one symbol. Qty
// is signed; AvgCost, Mark and Realized use the same tick and notional units as
// the in-memory risk ledger. UpdatedAt should be set by the caller that takes
// the snapshot so a restart does not rewrite the timestamp of unchanged rows.
type Position struct {
	AccountID uint64
	Symbol    string
	Qty       int64
	AvgCost   int64
	Mark      int64
	Realized  int64
	UpdatedAt time.Time
}
