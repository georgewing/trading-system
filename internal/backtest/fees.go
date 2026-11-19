package backtest

import "math"

// FeeModel charges basis points of traded notional. Notional is price ticks
// multiplied by quantity lots, so the fee is in the same integer unit as cash.
type FeeModel struct {
	MakerBps int64
	TakerBps int64
}

// Fee returns the fee for one fill, rounded down. Non-positive inputs and a
// non-positive bps produce zero. Overflow saturates instead of wrapping.
func (m FeeModel) Fee(price, qty int64, maker bool) int64 {
	bps := m.TakerBps
	if maker {
		bps = m.MakerBps
	}
	if price <= 0 || qty <= 0 || bps <= 0 {
		return 0
	}
	if price > math.MaxInt64/qty {
		return math.MaxInt64
	}
	notional := price * qty
	if notional > math.MaxInt64/bps {
		return math.MaxInt64
	}
	return notional * bps / 10000
}
