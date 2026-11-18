// Package orders keeps a queryable view of every order the gateway accepted,
// merging the submission details with the execution reports produced by the
// matching pipeline.
//
// The matching engine keeps its own limit-order-book state. This package is a
// read model only: it never influences matching.
package orders

import (
	"context"
	"sync"
	"time"

	"trading-system/internal/engine"
)

// Record is one order as shown to a client: submission fields plus the latest
// execution state.
type Record struct {
	OrderID       uint64
	ClientOrderID string
	AccountID     uint64
	Symbol        string
	Side          engine.Side
	Type          engine.OrderType
	TIF           engine.TimeInForce
	Price         int64  // integer ticks, see config matcher.price_precision
	Quantity      string // decimal string as submitted

	Status    engine.OrderStatus
	Filled    int64
	Leaves    int64
	Reason    string
	CreatedAt int64 // unix milliseconds
	UpdatedAt int64
}

// Book is a bounded, concurrency-safe collection of recent orders.
type Book struct {
	cap int

	mu    sync.RWMutex
	byID  map[uint64]*Record
	order []uint64 // insertion order, oldest first
}

// NewBook builds a book retaining at most capacity orders. A non-positive
// capacity falls back to 10000.
func NewBook(capacity int) *Book {
	if capacity <= 0 {
		capacity = 10000
	}
	return &Book{
		cap:  capacity,
		byID: make(map[uint64]*Record, capacity),
	}
}

// Add records a newly accepted order. Re-adding the same order id is ignored so
// retries cannot duplicate rows.
func (b *Book) Add(order engine.Order, nowMillis int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.byID[order.ID]; exists {
		return
	}

	rec := &Record{
		OrderID:       order.ID,
		ClientOrderID: order.ClientOrderID,
		AccountID:     order.AccountID,
		Symbol:        order.Symbol,
		Side:          order.Side,
		Type:          order.Type,
		TIF:           order.TIF,
		Price:         order.Price,
		Quantity:      order.Quantity,
		Status:        engine.OrderStatusNew,
		CreatedAt:     nowMillis,
		UpdatedAt:     nowMillis,
	}
	b.byID[order.ID] = rec
	b.order = append(b.order, order.ID)
	b.trimLocked()
}

// AddOrder records an order using the current wall clock.
func (b *Book) AddOrder(order engine.Order) {
	b.Add(order, nowMillis())
}

// Apply merges an execution report into the corresponding record. Reports for
// unknown orders (for example an order rejected before it was recorded) create
// a minimal placeholder so the client still sees the rejection.
func (b *Book) Apply(report engine.ExecReport, nowMillis int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	rec, ok := b.byID[report.OrderID]
	if !ok {
		rec = &Record{
			OrderID:   report.OrderID,
			Status:    report.Status,
			CreatedAt: nowMillis,
		}
		b.byID[report.OrderID] = rec
		b.order = append(b.order, report.OrderID)
		b.trimLocked()
	}

	rec.Status = report.Status
	rec.Filled = report.Filled
	rec.Leaves = report.Leaves
	rec.Reason = report.Reason
	rec.UpdatedAt = nowMillis
}

// InsertExecReport makes Book satisfy the pipeline storage sink contract.
func (b *Book) InsertExecReport(_ context.Context, report engine.ExecReport) error {
	b.Apply(report, nowMillis())
	return nil
}

// Get returns a copy of one order record.
func (b *Book) Get(orderID uint64) (Record, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	rec, ok := b.byID[orderID]
	if !ok {
		return Record{}, false
	}
	return *rec, true
}

// Recent returns up to limit records, newest first.
func (b *Book) Recent(limit int) []Record {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if limit <= 0 || limit > len(b.order) {
		limit = len(b.order)
	}

	out := make([]Record, 0, limit)
	for i := len(b.order) - 1; i >= 0 && len(out) < limit; i-- {
		if rec, ok := b.byID[b.order[i]]; ok {
			out = append(out, *rec)
		}
	}
	return out
}

// Len returns the number of retained orders.
func (b *Book) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.order)
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// StatusName maps the engine status enum to the wire contract used by HTTP and
// WebSocket clients.
func StatusName(s engine.OrderStatus) string {
	switch s {
	case engine.OrderStatusNew:
		return "NEW"
	case engine.OrderStatusPartiallyFilled:
		return "PARTIALLY_FILLED"
	case engine.OrderStatusFilled:
		return "FILLED"
	case engine.OrderStatusCanceled:
		return "CANCELED"
	case engine.OrderStatusRejected:
		return "REJECTED"
	default:
		return "UNKNOWN"
	}
}

// SideName maps the engine side enum to "BUY" or "SELL".
func SideName(s engine.Side) string {
	if s == engine.SideBuy {
		return "BUY"
	}
	return "SELL"
}

// trimLocked evicts the oldest records once the book exceeds its capacity.
func (b *Book) trimLocked() {
	if len(b.order) <= b.cap {
		return
	}
	drop := len(b.order) - b.cap
	for _, id := range b.order[:drop] {
		delete(b.byID, id)
	}
	b.order = append(b.order[:0], b.order[drop:]...)
}
