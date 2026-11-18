// Package backtest provides deterministic historical replay and simulated
// execution for strategies.
//
// The simulator trades in the same integer units as the matching engine: prices
// are ticks and quantities are lots. Every timestamp comes from the replay
// source, and every stochastic decision comes from a seeded generator, so a
// replay is reproducible byte for byte for the same inputs.
//
// What the simulator does not model, and callers must keep in mind when reading
// a report:
//
//   - queue position is a probability, not a real order queue;
//   - resting orders are filled from public trade prints and book moves, not
//     from other participants' order flow;
//   - latency between submission and matching is zero;
//   - the venue has no rejection other than the simulation's own limits.
package backtest

import "trading-system/internal/hft/strategy"

// Kind identifies an event in the replay stream.
type Kind uint8

const (
	KindBook Kind = iota + 1
	KindTrade
)

// String returns the stable wire name used by JSONL files.
func (k Kind) String() string {
	switch k {
	case KindBook:
		return "book"
	case KindTrade:
		return "trade"
	default:
		return "unknown"
	}
}

// Trade is one public trade print.
type Trade struct {
	ID    uint64
	Price int64
	Qty   int64
}

// BookSnapshot is a full depth snapshot. Bids are price descending and asks are
// price ascending; the source is responsible for ordering.
type BookSnapshot struct {
	Bids []strategy.Level
	Asks []strategy.Level
}

// Event is one replay unit. Exactly one of Book or Trade is set.
type Event struct {
	TS     int64 // unix milliseconds
	Symbol string
	Kind   Kind
	Book   *BookSnapshot
	Trade  *Trade
}
