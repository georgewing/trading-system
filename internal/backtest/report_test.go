package backtest

import (
	"bytes"
	"strings"
	"testing"

	"trading-system/internal/engine"
	"trading-system/internal/strategy"
)

// fillAt builds a zero-fee fill for portfolio tests.
func fillAt(symbol string, side engine.Side, price, qty int64) strategy.Fill {
	return strategy.Fill{Symbol: symbol, Side: side, Price: price, Qty: qty}
}

func TestBarsKeepsLastPointPerBucket(t *testing.T) {
	points := []EquityPoint{
		{TS: 0, Equity: 100},
		{TS: 10, Equity: 110},
		{TS: 59, Equity: 120},
		{TS: 60, Equity: 130},
		{TS: 61, Equity: 140},
	}
	got := Bars(points, 60)
	if len(got) != 2 {
		t.Fatalf("bars = %d, want 2: %+v", len(got), got)
	}
	if got[0].TS != 59 || got[0].Equity != 120 {
		t.Fatalf("first bar = %+v, want ts 59 equity 120", got[0])
	}
	if got[1].TS != 61 || got[1].Equity != 140 {
		t.Fatalf("second bar = %+v, want ts 61 equity 140", got[1])
	}
}

func TestMaxDrawdownBps(t *testing.T) {
	points := []EquityPoint{
		{TS: 0, Equity: 1000},
		{TS: 1, Equity: 1200},
		{TS: 2, Equity: 900},
		{TS: 3, Equity: 1100},
	}
	if got, want := MaxDrawdownBps(points), int64(2500); got != want {
		t.Fatalf("max drawdown = %d, want %d", got, want)
	}
	if got := MaxDrawdownBps(nil); got != 0 {
		t.Fatalf("empty drawdown = %d, want 0", got)
	}
}

func TestSharpeHandlesDegenerateCurves(t *testing.T) {
	flat := []EquityPoint{{TS: 0, Equity: 100}, {TS: 60_000, Equity: 100}, {TS: 120_000, Equity: 100}}
	if got := Sharpe(flat, 60_000); got != 0 {
		t.Fatalf("flat sharpe = %v, want 0", got)
	}
	if got := Sharpe(nil, 60_000); got != 0 {
		t.Fatalf("empty sharpe = %v, want 0", got)
	}

	rising := []EquityPoint{{TS: 0, Equity: 100}, {TS: 60_000, Equity: 101}, {TS: 120_000, Equity: 103}}
	if got := Sharpe(rising, 60_000); got <= 0 {
		t.Fatalf("rising sharpe = %v, want positive", got)
	}
}

func TestBuildReport(t *testing.T) {
	pf := NewPortfolio(1000)
	pf.ApplyFill(fillAt("X", engine.SideBuy, 10, 10)) // buy 10 @ 10
	pf.Mark("X", 20)                                  // unrealized +100

	report := BuildReport(ReportInput{
		Strategy:  "unit",
		Symbols:   []string{"X"},
		Events:    3,
		BarMillis: 60_000,
		Portfolio: pf,
		Stats:     Stats{Submitted: 4, FilledOrders: 2, CanceledOrders: 1, Fills: 2},
		Equity: []EquityPoint{
			{TS: 0, Equity: 1000},
			{TS: 60_000, Equity: 1100},
		},
	})

	if report.FinalEquity != 1100 {
		t.Fatalf("final equity = %d, want 1100", report.FinalEquity)
	}
	if report.UnrealizedPnL != 100 {
		t.Fatalf("unrealized = %d, want 100", report.UnrealizedPnL)
	}
	if report.FillRateBps != 5000 {
		t.Fatalf("fill rate = %d, want 5000", report.FillRateBps)
	}
	if len(report.Assumptions) == 0 {
		t.Fatal("assumptions missing")
	}

	var buf bytes.Buffer
	if err := report.WriteJSON(&buf); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if !strings.Contains(buf.String(), `"strategy": "unit"`) {
		t.Fatalf("json missing strategy: %s", buf.String())
	}

	buf.Reset()
	if err := report.WriteMarkdown(&buf); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	if !strings.Contains(buf.String(), "# Backtest Report: unit") {
		t.Fatalf("markdown missing title: %s", buf.String())
	}
}

func TestPortfolioRealizedPnL(t *testing.T) {
	pf := NewPortfolio(10_000)
	pf.ApplyFill(fillAt("X", engine.SideBuy, 100, 10)) // buy 10 @ 100
	pf.ApplyFill(fillAt("X", engine.SideSell, 110, 4)) // sell 4 @ 110

	if got := pf.Position("X"); got != 6 {
		t.Fatalf("position = %d, want 6", got)
	}
	if got := pf.Realized(); got != 40 {
		t.Fatalf("realized = %d, want 40", got)
	}
	pf.ApplyFill(fillAt("X", engine.SideSell, 120, 6)) // sell the rest @ 120
	if got := pf.Realized(); got != 40+120 {
		t.Fatalf("realized after close = %d, want 160", got)
	}
	if wins, total := pf.ClosedTrades(); wins != 2 || total != 2 {
		t.Fatalf("closed trades = %d/%d, want 2/2", wins, total)
	}
}
