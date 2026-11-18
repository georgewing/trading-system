package backtest

import (
	"math"
	"time"
)

// Bars downsamples an equity curve to the last observation per bucket. A
// non-positive width returns a copy unchanged.
func Bars(points []EquityPoint, barMillis int64) []EquityPoint {
	if barMillis <= 0 || len(points) == 0 {
		out := make([]EquityPoint, len(points))
		copy(out, points)
		return out
	}

	out := make([]EquityPoint, 0, len(points))
	var lastBucket int64 = math.MinInt64
	for _, p := range points {
		bucket := p.TS / barMillis
		if bucket == lastBucket {
			out[len(out)-1] = p
			continue
		}
		lastBucket = bucket
		out = append(out, p)
	}
	return out
}

// MaxDrawdownBps returns the largest peak-to-trough decline in basis points.
func MaxDrawdownBps(points []EquityPoint) int64 {
	var peak, worst int64
	for i, p := range points {
		if i == 0 || p.Equity > peak {
			peak = p.Equity
		}
		if peak <= 0 {
			continue
		}
		dd := (peak - p.Equity) * 10000 / peak
		if dd > worst {
			worst = dd
		}
	}
	return worst
}

// Sharpe returns the annualized Sharpe ratio of bar returns. It returns 0 when
// there are fewer than two bars or returns have no dispersion.
func Sharpe(points []EquityPoint, barMillis int64) float64 {
	if barMillis <= 0 {
		return 0
	}
	bars := Bars(points, barMillis)
	if len(bars) < 2 {
		return 0
	}

	returns := make([]float64, 0, len(bars)-1)
	for i := 1; i < len(bars); i++ {
		prev := float64(bars[i-1].Equity)
		if prev == 0 {
			continue
		}
		returns = append(returns, (float64(bars[i].Equity)-prev)/prev)
	}
	if len(returns) == 0 {
		return 0
	}

	var sum float64
	for _, r := range returns {
		sum += r
	}
	mean := sum / float64(len(returns))

	var sq float64
	for _, r := range returns {
		d := r - mean
		sq += d * d
	}
	std := math.Sqrt(sq / float64(len(returns)))
	if std == 0 {
		return 0
	}

	barsPerYear := float64(365*24*time.Hour/time.Millisecond) / float64(barMillis)
	return mean / std * math.Sqrt(barsPerYear)
}

// AverageEquity returns the mean equity across the sampled bars.
func AverageEquity(points []EquityPoint, barMillis int64) float64 {
	bars := Bars(points, barMillis)
	if len(bars) == 0 {
		return 0
	}
	var sum float64
	for _, p := range bars {
		sum += float64(p.Equity)
	}
	return sum / float64(len(bars))
}
