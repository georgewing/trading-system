package backtest

import "math"

// The simulator works with int64 ticks and lots. These helpers saturate instead
// of wrapping, so a pathological input produces an obviously wrong large number
// rather than a silently negative balance.
func addSat(a, b int64) int64 {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		if b > 0 {
			return math.MaxInt64
		}
		return math.MinInt64
	}
	return sum
}

func subSat(a, b int64) int64 {
	if b == math.MinInt64 {
		if a >= 0 {
			return math.MaxInt64
		}
		return a - b
	}
	return addSat(a, -b)
}

func mulSat(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > 0 && b > 0 && a > math.MaxInt64/b {
		return math.MaxInt64
	}
	if a < 0 && b < 0 && a < math.MaxInt64/b {
		return math.MaxInt64
	}
	if a > 0 && b < 0 && b < math.MinInt64/a {
		return math.MinInt64
	}
	if a < 0 && b > 0 && a < math.MinInt64/b {
		return math.MinInt64
	}
	return a * b
}

func abs64(v int64) int64 {
	if v == math.MinInt64 {
		return math.MaxInt64
	}
	if v < 0 {
		return -v
	}
	return v
}
