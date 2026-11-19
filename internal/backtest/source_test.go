package backtest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONLSourceReadsSample(t *testing.T) {
	src, err := OpenJSONL("testdata/sample.jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()

	var ev Event
	count := 0
	var first Event
	var firstTrade Event

	for {
		ok, err := src.Next(context.Background(), &ev)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if !ok {
			break
		}
		if count == 0 {
			first = ev
		}
		if ev.Kind == KindTrade && firstTrade.Kind == 0 {
			firstTrade = ev
		}
		count++
	}

	if count != 14 {
		t.Fatalf("events = %d, want 14", count)
	}
	if first.Kind != KindBook || first.Symbol != "BTCUSDT" || first.TS != 1700000000000 {
		t.Fatalf("first event = %+v", first)
	}
	if first.Book == nil || len(first.Book.Bids) != 1 || first.Book.Bids[0].Price != 10000 {
		t.Fatalf("first book = %+v", first.Book)
	}
	if firstTrade.Trade == nil || firstTrade.Trade.Price != 9997 || firstTrade.Trade.Qty != 100000000 {
		t.Fatalf("first trade = %+v", firstTrade.Trade)
	}
}

func TestJSONLSourceRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"invalid json", "{not json}\n"},
		{"missing symbol", `{"ts":1,"kind":"book","book":{"bids":[],"asks":[]}}` + "\n"},
		{"unknown kind", `{"ts":1,"symbol":"X","kind":"quote"}` + "\n"},
		{"trade without payload", `{"ts":1,"symbol":"X","kind":"trade"}` + "\n"},
		{"timestamp goes backwards", `{"ts":2,"symbol":"X","kind":"book","book":{"bids":[],"asks":[]}}` + "\n" +
			`{"ts":1,"symbol":"X","kind":"book","book":{"bids":[],"asks":[]}}` + "\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			src, err := OpenJSONL(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer src.Close()

			var ev Event
			for {
				ok, err := src.Next(context.Background(), &ev)
				if err != nil {
					return // expected
				}
				if !ok {
					t.Fatal("expected an error, stream ended cleanly")
				}
			}
		})
	}
}

func TestSliceSourceHonoursContext(t *testing.T) {
	src := NewSliceSource([]Event{{TS: 1, Symbol: "X", Kind: KindBook}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var ev Event
	if _, err := src.Next(ctx, &ev); err == nil {
		t.Fatal("expected context error")
	}
}
