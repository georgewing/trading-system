package backtest

import (
	"trading-system/internal/hft/engine"
	"trading-system/internal/strategy"
)

// EquityPoint is one mark-to-market observation.
type EquityPoint struct {
	TS     int64
	Equity int64
}

// position is the simulator's cost-basis view of one symbol. AvgCost is in
// price ticks; Realized is in notional units and excludes fees.
type position struct {
	qty     int64
	avgCost int64
	mark    int64
}

// Portfolio tracks cash, positions and the metrics the report needs.
//
// Cash falls by notional plus fee on a buy and rises by notional minus fee on a
// sell. Equity is cash plus marked positions, so fees are already reflected in
// equity while Realized is reported gross of fees.
type Portfolio struct {
	initialCash int64
	cash        int64
	fees        int64
	realized    int64
	turnover    int64
	maxExposure int64

	positions map[string]*position
	closed    int
	wins      int
}

// NewPortfolio builds a portfolio with the given starting cash.
func NewPortfolio(initialCash int64) *Portfolio {
	return &Portfolio{
		initialCash: initialCash,
		cash:        initialCash,
		positions:   make(map[string]*position),
	}
}

// ApplyFill books one fill.
func (p *Portfolio) ApplyFill(f strategy.Fill) {
	// Cash and turnover.
	notional := mulSat(f.Price, f.Qty)
	p.turnover = addSat(p.turnover, abs64(notional))
	p.fees = addSat(p.fees, f.Fee)

	if f.Side == engine.SideBuy {
		p.cash = subSat(p.cash, addSat(notional, f.Fee))
	} else {
		p.cash = addSat(p.cash, subSat(notional, f.Fee))
	}

	pos := p.positions[f.Symbol]
	if pos == nil {
		pos = &position{}
		p.positions[f.Symbol] = pos
	}

	delta := f.Qty
	if f.Side == engine.SideSell {
		delta = -delta
	}
	p.applyDelta(pos, delta, f.Price)

	pos.mark = f.Price
	p.updateExposure()
}

func (p *Portfolio) applyDelta(pos *position, delta, price int64) {
	oldQty := pos.qty

	// Opening or adding in the same direction: update the weighted average cost.
	if oldQty == 0 || (oldQty > 0) == (delta > 0) {
		newQty := addSat(oldQty, delta)
		absOld := abs64(oldQty)
		absDelta := abs64(delta)
		absNew := abs64(newQty)
		if absNew == 0 {
			pos.avgCost = 0
		} else {
			cost := addSat(mulSat(pos.avgCost, absOld), mulSat(price, absDelta))
			pos.avgCost = cost / absNew
		}
		pos.qty = newQty
		return
	}

	// Reducing or flipping: realize PnL on the closed quantity.
	closing := abs64(oldQty)
	if abs64(delta) < closing {
		closing = abs64(delta)
	}
	var pnl int64
	if oldQty > 0 {
		pnl = mulSat(subSat(price, pos.avgCost), closing)
	} else {
		pnl = mulSat(subSat(pos.avgCost, price), closing)
	}
	p.realized = addSat(p.realized, pnl)
	p.closed++
	if pnl > 0 {
		p.wins++
	}

	newQty := addSat(oldQty, delta)
	switch {
	case newQty == 0:
		pos.avgCost = 0
	case (newQty > 0) != (oldQty > 0):
		// Flipped through zero: the remainder opens at the fill price.
		pos.avgCost = price
	}
	pos.qty = newQty
}

// Mark records the latest price for symbol.
func (p *Portfolio) Mark(symbol string, price int64) {
	if price <= 0 {
		return
	}
	pos := p.positions[symbol]
	if pos == nil {
		pos = &position{}
		p.positions[symbol] = pos
	}
	pos.mark = price
	p.updateExposure()
}

// Equity returns cash plus marked positions.
func (p *Portfolio) Equity() int64 {
	eq := p.cash
	for _, pos := range p.positions {
		if pos.qty == 0 || pos.mark == 0 {
			continue
		}
		eq = addSat(eq, mulSat(pos.qty, pos.mark))
	}
	return eq
}

// Unrealized returns the marked PnL of open positions.
func (p *Portfolio) Unrealized() int64 {
	var pnl int64
	for _, pos := range p.positions {
		if pos.qty == 0 || pos.mark == 0 {
			continue
		}
		pnl = addSat(pnl, mulSat(pos.qty, subSat(pos.mark, pos.avgCost)))
	}
	return pnl
}

// Position returns the signed position for symbol.
func (p *Portfolio) Position(symbol string) int64 {
	if pos := p.positions[symbol]; pos != nil {
		return pos.qty
	}
	return 0
}

// Realized returns closed PnL gross of fees.
func (p *Portfolio) Realized() int64 { return p.realized }

// Fees returns total fees paid.
func (p *Portfolio) Fees() int64 { return p.fees }

// Turnover returns total traded notional.
func (p *Portfolio) Turnover() int64 { return p.turnover }

// InitialCash returns the starting cash.
func (p *Portfolio) InitialCash() int64 { return p.initialCash }

// MaxGrossExposure returns the largest sum of absolute marked positions seen.
func (p *Portfolio) MaxGrossExposure() int64 { return p.maxExposure }

// ClosedTrades returns the number of closing fills and how many were profitable.
func (p *Portfolio) ClosedTrades() (wins, total int) { return p.wins, p.closed }

func (p *Portfolio) updateExposure() {
	var gross int64
	for _, pos := range p.positions {
		if pos.qty == 0 || pos.mark == 0 {
			continue
		}
		gross = addSat(gross, abs64(mulSat(pos.qty, pos.mark)))
	}
	if gross > p.maxExposure {
		p.maxExposure = gross
	}
}
