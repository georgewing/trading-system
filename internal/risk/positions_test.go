package risk

import (
	"testing"

	"trading-system/internal/engine"
)

func TestPositionsAverageCostAndRealized(t *testing.T) {
	p := NewPositions()

	p.ApplyFill(7, "X", engine.SideBuy, 100, 10)
	pos := p.Snapshot(7, "X")
	if pos.Qty != 10 || pos.AvgCost != 100 || pos.Realized != 0 {
		t.Fatalf("after buy: %+v", pos)
	}

	p.ApplyFill(7, "X", engine.SideSell, 110, 4)
	pos = p.Snapshot(7, "X")
	if pos.Qty != 6 {
		t.Fatalf("qty = %d, want 6", pos.Qty)
	}
	if pos.AvgCost != 100 {
		t.Fatalf("avg cost = %d, want 100", pos.AvgCost)
	}
	if pos.Realized != 40 {
		t.Fatalf("realized = %d, want 40", pos.Realized)
	}

	p.ApplyFill(7, "X", engine.SideSell, 120, 6)
	pos = p.Snapshot(7, "X")
	if pos.Qty != 0 || pos.AvgCost != 0 {
		t.Fatalf("after close: %+v", pos)
	}
	if pos.Realized != 160 {
		t.Fatalf("realized = %d, want 160", pos.Realized)
	}

	wins, total := p.ClosedTrades()
	if wins != 2 || total != 2 {
		t.Fatalf("closed = %d/%d, want 2/2", wins, total)
	}
}

func TestPositionsFlipThroughZero(t *testing.T) {
	p := NewPositions()
	p.ApplyFill(7, "X", engine.SideBuy, 100, 5)
	p.ApplyFill(7, "X", engine.SideSell, 110, 8)

	pos := p.Snapshot(7, "X")
	if pos.Qty != -3 {
		t.Fatalf("qty = %d, want -3", pos.Qty)
	}
	if pos.AvgCost != 110 {
		t.Fatalf("avg cost = %d, want 110", pos.AvgCost)
	}
	if pos.Realized != 50 {
		t.Fatalf("realized = %d, want 50", pos.Realized)
	}
}

func TestPositionsMarkAndEquity(t *testing.T) {
	p := NewPositions()
	p.ApplyFill(7, "X", engine.SideBuy, 100, 10)
	p.Mark("X", 120)

	if got := p.AccountEquity(7); got != 200 {
		t.Fatalf("equity = %d, want 200", got)
	}
	if got := p.AccountEquity(8); got != 0 {
		t.Fatalf("other account equity = %d, want 0", got)
	}
}

func TestPositionsIgnoreInvalidFills(t *testing.T) {
	p := NewPositions()
	p.ApplyFill(0, "X", engine.SideBuy, 100, 1)
	p.ApplyFill(7, "", engine.SideBuy, 100, 1)
	p.ApplyFill(7, "X", engine.SideBuy, 0, 1)
	p.ApplyFill(7, "X", engine.SideBuy, 100, 0)

	if got := p.Qty(7, "X"); got != 0 {
		t.Fatalf("qty = %d, want 0", got)
	}
}
