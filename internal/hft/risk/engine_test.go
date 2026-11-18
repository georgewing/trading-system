package risk

import (
	"context"
	"testing"
	"time"

	"trading-system/internal/hft/engine"
)

type fakeSink struct{ events []Event }

func (f *fakeSink) Record(ev Event) { f.events = append(f.events, ev) }

type fakeMetrics struct {
	events []string
	states []string
	drops  int
}

func (f *fakeMetrics) IncRiskEvent(code, action string) {
	f.events = append(f.events, code+":"+action)
}

func (f *fakeMetrics) SetStrategyState(strategy, state string) {
	f.states = append(f.states, strategy+":"+state)
}

func (f *fakeMetrics) IncRiskEventDrop() { f.drops++ }

func engineRequest() Request {
	return Request{
		AccountID:     7,
		ClientOrderID: "c-1",
		Symbol:        "X",
		Side:          engine.SideBuy,
		Type:          engine.OrderTypeLimit,
		TIF:           engine.TimeInForceGTC,
		Price:         100,
		Qty:           10,
		Now:           time.Unix(1_700_000_000, 0),
	}
}

// TestEngineUsesPositionLedgerForMaxPosition is the regression test for the
// original bug: the pipeline never populated CurrentPosition, so max_position
// was always evaluated against zero.
func TestEngineUsesPositionLedgerForMaxPosition(t *testing.T) {
	pos := NewPositions()
	pos.ApplyFill(7, "X", engine.SideBuy, 100, 95)

	e := NewEngine(EngineConfig{PreTrade: Config{MaxPosition: 100}}, pos)

	req := engineRequest()
	req.Qty = 10
	req.CurrentPosition = -1000 // must be ignored in favour of the ledger

	if got := e.Check(context.Background(), req); got.Approved || got.Code != CodeMaxPosition {
		t.Fatalf("decision = %+v, want %s", got, CodeMaxPosition)
	}

	req.Qty = 5
	if got := e.Check(context.Background(), req); !got.Approved {
		t.Fatalf("within limit: %+v", got)
	}
}

func TestEngineUsesInjectedReferencePrice(t *testing.T) {
	e := NewEngine(EngineConfig{PreTrade: Config{PriceBandBps: 100}}, nil,
		WithReferencePrice(func(string) int64 { return 10000 }))

	req := engineRequest()
	req.Price = 10101
	if got := e.Check(context.Background(), req); got.Code != CodePriceBand {
		t.Fatalf("decision = %+v, want %s", got, CodePriceBand)
	}

	req.Price = 10100
	if got := e.Check(context.Background(), req); !got.Approved {
		t.Fatalf("boundary decision = %+v", got)
	}
}

func TestEngineEmitsRejectionEvent(t *testing.T) {
	sink := &fakeSink{}
	met := &fakeMetrics{}
	pos := NewPositions()
	pos.ApplyFill(7, "X", engine.SideBuy, 100, 95)

	e := NewEngine(EngineConfig{PreTrade: Config{MaxPosition: 100}}, pos,
		WithEvents(sink), WithMetrics(met))

	if got := e.Check(context.Background(), engineRequest()); got.Approved {
		t.Fatalf("expected rejection: %+v", got)
	}

	if len(sink.events) != 1 || sink.events[0].Code != CodeMaxPosition {
		t.Fatalf("sink events = %+v", sink.events)
	}
	if sink.events[0].AccountID != 7 || sink.events[0].Symbol != "X" {
		t.Fatalf("event identifiers = %+v", sink.events[0])
	}
	if len(met.events) != 1 || met.events[0] != CodeMaxPosition+":"+string(ActionReject) {
		t.Fatalf("metrics events = %+v", met.events)
	}
}

func TestEngineAppliesFillsAndTripsDailyLoss(t *testing.T) {
	pos := NewPositions()
	sink := &fakeSink{}
	met := &fakeMetrics{}

	e := NewEngine(EngineConfig{Intraday: IntradayConfig{MaxDailyLoss: 50}}, pos,
		WithEvents(sink), WithMetrics(met))

	buy := engine.Order{ID: 1, AccountID: 7, Symbol: "X", Side: engine.SideBuy}
	e.OnExecReport(buy, engine.ExecReport{
		OrderID: 1,
		Status:  engine.OrderStatusFilled,
		Trade:   &engine.Trade{Price: 100, Quantity: 10},
	})

	sell := buy
	sell.Side = engine.SideSell
	e.OnExecReport(sell, engine.ExecReport{
		OrderID: 2,
		Status:  engine.OrderStatusFilled,
		Trade:   &engine.Trade{Price: 90, Quantity: 10},
	})

	if got := pos.Qty(7, "X"); got != 0 {
		t.Fatalf("qty = %d, want 0", got)
	}
	if got := pos.Snapshot(7, "X").Realized; got != -100 {
		t.Fatalf("realized = %d, want -100", got)
	}

	found := false
	for _, ev := range sink.events {
		if ev.Code == CodeDailyLoss && ev.Action == ActionHalt {
			found = true
		}
	}
	if !found {
		t.Fatalf("daily loss event missing: %+v", sink.events)
	}

	if got := e.Check(context.Background(), engineRequest()); got.Code != CodeStrategyHalted {
		t.Fatalf("decision = %+v, want %s", got, CodeStrategyHalted)
	}
}

func TestEngineTerminalReportReleasesOpenOrder(t *testing.T) {
	pos := NewPositions()
	e := NewEngine(EngineConfig{Intraday: IntradayConfig{MaxOpenOrders: 1}}, pos)

	order := engine.Order{ID: 1, AccountID: 7, Symbol: "X", Side: engine.SideBuy}
	e.OnOrderAccepted(order)

	if got := e.Check(context.Background(), engineRequest()); got.Code != CodeMaxOpenOrders {
		t.Fatalf("decision = %+v, want %s", got, CodeMaxOpenOrders)
	}

	e.OnExecReport(order, engine.ExecReport{OrderID: 1, Status: engine.OrderStatusFilled})

	if got := e.Check(context.Background(), engineRequest()); !got.Approved {
		t.Fatalf("after terminal report: %+v", got)
	}
}

// TestEngineAppliesMakerFillToMakerAccount is the regression test for the
// missing maker leg: the taker report carries the resting account and side, so
// both ledgers must move after one fill.
func TestEngineAppliesMakerFillToMakerAccount(t *testing.T) {
	const (
		takerAccount = uint64(7)
		makerAccount = uint64(9)
	)

	cases := []struct {
		name      string
		takerSide engine.Side
		makerSide engine.Side
		wantTaker int64
		wantMaker int64
	}{
		{"taker buy maker sell", engine.SideBuy, engine.SideSell, 10, -10},
		{"taker sell maker buy", engine.SideSell, engine.SideBuy, -10, 10},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pos := NewPositions()
			e := NewEngine(EngineConfig{}, pos)

			order := engine.Order{ID: 1, AccountID: takerAccount, Symbol: "X", Side: tc.takerSide}
			e.OnExecReport(order, engine.ExecReport{
				OrderID: 1,
				Status:  engine.OrderStatusFilled,
				Trade: &engine.Trade{
					ID:             100,
					Symbol:         "X",
					Price:          100,
					Quantity:       10,
					MakerAccountID: makerAccount,
					MakerSide:      tc.makerSide,
				},
			})

			taker := pos.Snapshot(takerAccount, "X")
			if taker.Qty != tc.wantTaker || taker.AvgCost != 100 || taker.Mark != 100 {
				t.Fatalf("taker position = %+v, want qty=%d avg=100 mark=100", taker, tc.wantTaker)
			}

			maker := pos.Snapshot(makerAccount, "X")
			if maker.Qty != tc.wantMaker || maker.AvgCost != 100 || maker.Mark != 100 {
				t.Fatalf("maker position = %+v, want qty=%d avg=100 mark=100", maker, tc.wantMaker)
			}
		})
	}
}

// TestEngineSkipsMakerLegForSelfTrade guards the double-counting case: when the
// resting account is the taker account, only the taker fill may be booked.
func TestEngineSkipsMakerLegForSelfTrade(t *testing.T) {
	pos := NewPositions()
	e := NewEngine(EngineConfig{}, pos)

	order := engine.Order{ID: 1, AccountID: 7, Symbol: "X", Side: engine.SideBuy}
	e.OnExecReport(order, engine.ExecReport{
		OrderID: 1,
		Status:  engine.OrderStatusFilled,
		Trade: &engine.Trade{
			Price:          100,
			Quantity:       10,
			MakerAccountID: 7,
			MakerSide:      engine.SideSell,
		},
	})

	if got := pos.Snapshot(7, "X"); got.Qty != 10 || got.Realized != 0 {
		t.Fatalf("self-trade position = %+v, want qty 10 and no realized PnL", got)
	}
}

// TestEngineSkipsMakerLegWithInvalidSide guards the zero and out-of-range
// MakerSide values that venues without counterparty data produce.
func TestEngineSkipsMakerLegWithInvalidSide(t *testing.T) {
	for _, side := range []engine.Side{0, 2, -2} {
		pos := NewPositions()
		e := NewEngine(EngineConfig{}, pos)

		order := engine.Order{ID: 1, AccountID: 7, Symbol: "X", Side: engine.SideBuy}
		e.OnExecReport(order, engine.ExecReport{
			OrderID: 1,
			Status:  engine.OrderStatusFilled,
			Trade: &engine.Trade{
				Price:          100,
				Quantity:       10,
				MakerAccountID: 9,
				MakerSide:      side,
			},
		})

		if got := pos.Qty(7, "X"); got != 10 {
			t.Fatalf("side %d: taker qty = %d, want 10", side, got)
		}
		if got := pos.Qty(9, "X"); got != 0 {
			t.Fatalf("side %d: maker qty = %d, want 0", side, got)
		}
	}
}
