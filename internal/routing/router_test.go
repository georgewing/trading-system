package routing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"trading-system/internal/engine"
	"trading-system/internal/execution"
)

type stubVenue struct {
	name string
}

func (s *stubVenue) Name() string { return s.name }

func (s *stubVenue) Submit(context.Context, engine.Order) (engine.SubmitResult, error) {
	return engine.SubmitResult{}, nil
}

func (s *stubVenue) Cancel(context.Context, execution.CancelRequest) error { return nil }

func newTestRouter(t *testing.T) (*Router, *stubVenue, *stubVenue) {
	t.Helper()

	paper := &stubVenue{name: "paper"}
	binance := &stubVenue{name: "binance"}
	r, err := NewRouter(
		"paper",
		map[string]execution.Venue{"paper": paper, "binance": binance},
		map[string]string{"BTCUSDT": "binance"},
	)
	if err != nil {
		t.Fatalf("NewRouter() error = %v, want nil", err)
	}
	return r, paper, binance
}

func TestRouterRouteSymbolMapping(t *testing.T) {
	r, _, binance := newTestRouter(t)

	venue, err := r.Route("BTCUSDT")
	if err != nil {
		t.Fatalf("Route(BTCUSDT) error = %v, want nil", err)
	}
	if venue.Name() != binance.Name() {
		t.Fatalf("Route(BTCUSDT) = %q, want %q", venue.Name(), binance.Name())
	}
}

func TestRouterRouteDefaultFallback(t *testing.T) {
	r, paper, _ := newTestRouter(t)

	venue, err := r.Route("ETHUSDT")
	if err != nil {
		t.Fatalf("Route(ETHUSDT) error = %v, want nil", err)
	}
	if venue.Name() != paper.Name() {
		t.Fatalf("Route(ETHUSDT) = %q, want %q", venue.Name(), paper.Name())
	}
}

func TestRouterRouteUnknownSymbol(t *testing.T) {
	var zero Router

	if _, err := zero.Route("ETHUSDT"); !errors.Is(err, ErrUnknownSymbol) {
		t.Fatalf("Route() error = %v, want %v", err, ErrUnknownSymbol)
	}
}

func TestRouterRouteUnknownVenue(t *testing.T) {
	r := &Router{
		defaultVenue: "paper",
		venues:       map[string]execution.Venue{"paper": &stubVenue{name: "paper"}},
		symbolVenues: map[string]string{"BTCUSDT": "binance"},
	}

	if _, err := r.Route("BTCUSDT"); !errors.Is(err, ErrUnknownVenue) {
		t.Fatalf("Route() error = %v, want %v", err, ErrUnknownVenue)
	}
}

func TestRouterDefault(t *testing.T) {
	r, paper, _ := newTestRouter(t)

	if got := r.Default(); got == nil || got.Name() != paper.Name() {
		t.Fatalf("Default() = %v, want %q", got, paper.Name())
	}
}

func TestNewRouterInvalidConfig(t *testing.T) {
	paper := &stubVenue{name: "paper"}
	validVenues := map[string]execution.Venue{"paper": paper}

	tests := []struct {
		name         string
		defaultVenue string
		venues       map[string]execution.Venue
		symbolVenues map[string]string
		wantErr      error
	}{
		{
			name:         "empty default venue",
			defaultVenue: "",
			venues:       validVenues,
		},
		{
			name:         "no venues",
			defaultVenue: "paper",
			venues:       nil,
		},
		{
			name:         "default venue not configured",
			defaultVenue: "binance",
			venues:       validVenues,
			wantErr:      ErrUnknownVenue,
		},
		{
			name:         "empty venue name",
			defaultVenue: "paper",
			venues:       map[string]execution.Venue{"": paper, "paper": paper},
		},
		{
			name:         "nil default venue",
			defaultVenue: "paper",
			venues:       map[string]execution.Venue{"paper": nil},
		},
		{
			name:         "nil non-default venue",
			defaultVenue: "paper",
			venues:       map[string]execution.Venue{"paper": paper, "binance": nil},
		},
		{
			name:         "symbol maps to unknown venue",
			defaultVenue: "paper",
			venues:       validVenues,
			symbolVenues: map[string]string{"BTCUSDT": "binance"},
			wantErr:      ErrUnknownVenue,
		},
		{
			name:         "symbol maps to empty venue",
			defaultVenue: "paper",
			venues:       validVenues,
			symbolVenues: map[string]string{"BTCUSDT": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewRouter(tt.defaultVenue, tt.venues, tt.symbolVenues)
			if err == nil {
				t.Fatalf("NewRouter() error = nil, want error")
			}
			if r != nil {
				t.Fatalf("NewRouter() router = %v, want nil on error", r)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewRouter() error = %v, want wrapping %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewRouterCopiesMaps(t *testing.T) {
	paper := &stubVenue{name: "paper"}
	binance := &stubVenue{name: "binance"}
	venues := map[string]execution.Venue{"paper": paper}
	symbols := map[string]string{"BTCUSDT": "paper"}

	r, err := NewRouter("paper", venues, symbols)
	if err != nil {
		t.Fatalf("NewRouter() error = %v, want nil", err)
	}

	// Mutating the caller maps must not affect the router.
	venues["binance"] = binance
	symbols["BTCUSDT"] = "binance"
	symbols["ETHUSDT"] = "binance"

	venue, err := r.Route("BTCUSDT")
	if err != nil {
		t.Fatalf("Route(BTCUSDT) error = %v, want nil", err)
	}
	if venue.Name() != "paper" {
		t.Fatalf("Route(BTCUSDT) = %q, want paper after caller map mutation", venue.Name())
	}

	venue, err = r.Route("ETHUSDT")
	if err != nil {
		t.Fatalf("Route(ETHUSDT) error = %v, want nil", err)
	}
	if venue.Name() != "paper" {
		t.Fatalf("Route(ETHUSDT) = %q, want default paper", venue.Name())
	}
}

func TestRouterConcurrentReads(t *testing.T) {
	r, _, _ := newTestRouter(t)

	const workers = 8
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				venue, err := r.Route("BTCUSDT")
				if err != nil || venue.Name() != "binance" {
					t.Errorf("Route(BTCUSDT) = (%v, %v), want binance", venue, err)
					return
				}
				if venue, err := r.Route("ETHUSDT"); err != nil || venue.Name() != "paper" {
					t.Errorf("Route(ETHUSDT) = (%v, %v), want paper", venue, err)
					return
				}
				if venue := r.Default(); venue == nil || venue.Name() != "paper" {
					t.Errorf("Default() = %v, want paper", venue)
					return
				}
			}
		}()
	}
	wg.Wait()
}
