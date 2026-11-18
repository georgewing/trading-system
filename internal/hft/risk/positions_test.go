package risk

import (
	"testing"

	"trading-system/internal/hft/engine"
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

func TestPositionsSnapshotAllDeepCopy(t *testing.T) {
	p := NewPositions()
	p.ApplyFill(7, "X", engine.SideBuy, 100, 10)
	p.ApplyFill(8, "Y", engine.SideSell, 200, 4)
	p.Mark("Y", 210)

	snap := p.SnapshotAll()
	if len(snap) != 2 {
		t.Fatalf("len(snapshot) = %d, want 2", len(snap))
	}
	wantY := p.Snapshot(8, "Y")

	// Later ledger activity must not leak into the snapshot.
	p.ApplyFill(7, "X", engine.SideBuy, 102, 90)
	p.Mark("Y", 260)

	if got := snap[PositionKey{AccountID: 7, Symbol: "X"}]; got.Qty != 10 || got.AvgCost != 100 {
		t.Fatalf("snapshot X = %+v, want qty 10 avg 100", got)
	}
	if got := snap[PositionKey{AccountID: 8, Symbol: "Y"}]; got != wantY {
		t.Fatalf("snapshot Y = %+v, want %+v", got, wantY)
	}

	// Mutating the returned map must not leak back into the ledger.
	mutated := snap[PositionKey{AccountID: 8, Symbol: "Y"}]
	mutated.Qty = 999
	snap[PositionKey{AccountID: 8, Symbol: "Y"}] = mutated
	if got := p.Qty(8, "Y"); got != -4 {
		t.Fatalf("ledger qty after snapshot mutation = %d, want -4", got)
	}
}

func TestPositionsRestoreRoundTrip(t *testing.T) {
	src := NewPositions()
	src.ApplyFill(7, "X", engine.SideBuy, 100, 10)
	src.ApplyFill(7, "X", engine.SideSell, 110, 4)
	src.ApplyFill(9, "Y", engine.SideSell, 50, 3)

	dst := NewPositions()
	dst.ApplyFill(11, "Z", engine.SideBuy, 5, 1) // replace semantics drop this
	dst.Restore(src.SnapshotAll())

	restored := dst.SnapshotAll()
	if len(restored) != 2 {
		t.Fatalf("len(restored) = %d, want 2", len(restored))
	}
	for key, want := range src.SnapshotAll() {
		if got := dst.Snapshot(key.AccountID, key.Symbol); got != want {
			t.Fatalf("restored %+v = %+v, want %+v", key, got, want)
		}
	}
	if got := dst.Qty(11, "Z"); got != 0 {
		t.Fatalf("stale position survived restore: qty = %d, want 0", got)
	}
	if got, want := dst.AccountEquity(7), src.AccountEquity(7); got != want {
		t.Fatalf("restored equity = %d, want %d", got, want)
	}

	// The restored ledger must keep working with the average-cost logic.
	dst.ApplyFill(7, "X", engine.SideBuy, 120, 6)
	pos := dst.Snapshot(7, "X")
	if pos.Qty != 12 || pos.AvgCost != 110 {
		t.Fatalf("after fill on restored position: %+v, want qty 12 avg 110", pos)
	}
}

func TestPositionsRestoreNilAndEmpty(t *testing.T) {
	p := NewPositions()
	p.ApplyFill(7, "X", engine.SideBuy, 100, 10)

	p.Restore(nil)
	if got := p.SnapshotAll(); len(got) != 0 {
		t.Fatalf("snapshot after nil restore = %+v, want empty", got)
	}
	if got := p.Qty(7, "X"); got != 0 {
		t.Fatalf("qty after nil restore = %d, want 0", got)
	}

	p.ApplyFill(7, "X", engine.SideBuy, 100, 10)
	p.Restore(map[PositionKey]Position{})
	if got := p.SnapshotAll(); len(got) != 0 {
		t.Fatalf("snapshot after empty restore = %+v, want empty", got)
	}
}

func TestPositionsRestoreSkipsInvalidEntries(t *testing.T) {
	p := NewPositions()
	p.Restore(map[PositionKey]Position{
		{AccountID: 0, Symbol: "X"}:  {Qty: 1, AvgCost: 100},
		{AccountID: 7, Symbol: ""}:   {Qty: 1, AvgCost: 100},
		{AccountID: 8, Symbol: "X"}:  {Qty: 1, AvgCost: -1},
		{AccountID: 9, Symbol: "X"}:  {Qty: 1, AvgCost: 100, Mark: -1},
		{AccountID: 10, Symbol: "X"}: {Qty: 5, AvgCost: 100, Mark: 101, Realized: 2},
	})

	if got := len(p.SnapshotAll()); got != 1 {
		t.Fatalf("len(restored) = %d, want 1", got)
	}
	pos := p.Snapshot(10, "X")
	if pos.Qty != 5 || pos.AvgCost != 100 || pos.Mark != 101 || pos.Realized != 2 {
		t.Fatalf("valid entry = %+v, want qty 5 avg 100 mark 101 realized 2", pos)
	}
}
