package risk

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"trading-system/internal/hft/engine"
)

func baseRequest() Request {
	return Request{
		AccountID:     42,
		ClientOrderID: "client-1",
		Symbol:        "BTC-USDT",
		Side:          engine.SideBuy,
		Type:          engine.OrderTypeLimit,
		TIF:           engine.TimeInForceGTC,
		Price:         10000,
		Qty:           10,
		Now:           time.Unix(1_700_000_000, 0),
	}
}

func TestGuardCheck(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelExpired()

	tests := []struct {
		name     string
		ctx      context.Context
		cfg      Config
		mutate   func(*Request)
		preCalls int
		wantCode string
	}{
		// Rule 1: context.
		{
			name:     "context canceled",
			ctx:      canceled,
			wantCode: CodeCanceled,
		},
		{
			name: "context canceled before invalid request",
			ctx:  canceled,
			mutate: func(r *Request) {
				r.AccountID = 0
			},
			wantCode: CodeCanceled,
		},
		{
			name:     "context deadline exceeded",
			ctx:      expired,
			wantCode: CodeCanceled,
		},

		// Rule 2: required fields.
		{name: "account id missing", mutate: func(r *Request) { r.AccountID = 0 }, wantCode: CodeInvalidRequest},
		{name: "client order id missing", mutate: func(r *Request) { r.ClientOrderID = "" }, wantCode: CodeInvalidRequest},
		{name: "symbol missing", mutate: func(r *Request) { r.Symbol = "" }, wantCode: CodeInvalidRequest},
		{name: "side zero", mutate: func(r *Request) { r.Side = 0 }, wantCode: CodeInvalidRequest},
		{name: "side unknown", mutate: func(r *Request) { r.Side = engine.Side(2) }, wantCode: CodeInvalidRequest},
		{name: "type unknown", mutate: func(r *Request) { r.Type = engine.OrderType("STOP") }, wantCode: CodeInvalidRequest},
		{name: "tif unknown", mutate: func(r *Request) { r.TIF = engine.TimeInForce("DAY") }, wantCode: CodeInvalidRequest},
		{name: "price zero", mutate: func(r *Request) { r.Price = 0 }, wantCode: CodeInvalidRequest},
		{name: "price negative", mutate: func(r *Request) { r.Price = -1 }, wantCode: CodeInvalidRequest},
		{name: "qty zero", mutate: func(r *Request) { r.Qty = 0 }, wantCode: CodeInvalidRequest},
		{name: "qty negative", mutate: func(r *Request) { r.Qty = -1 }, wantCode: CodeInvalidRequest},

		// Rule 3: symbol whitelist.
		{
			name:     "symbol allowed by whitelist",
			cfg:      Config{SymbolWhitelist: []string{"BTC-USDT"}},
			wantCode: CodeOK,
		},
		{
			name:     "symbol not in whitelist",
			cfg:      Config{SymbolWhitelist: []string{"ETH-USDT"}},
			wantCode: CodeSymbolNotAllowed,
		},
		{
			name: "empty whitelist allows any symbol",
			mutate: func(r *Request) {
				r.Symbol = "DOGE-USDT"
			},
			wantCode: CodeOK,
		},
		{
			name:     "whitelist is case sensitive",
			cfg:      Config{SymbolWhitelist: []string{"btc-usdt"}},
			wantCode: CodeSymbolNotAllowed,
		},

		// Rule 4: max order qty.
		{
			name:     "qty equals max",
			cfg:      Config{MaxOrderQty: 10},
			wantCode: CodeOK,
		},
		{
			name:     "qty over max",
			cfg:      Config{MaxOrderQty: 9},
			wantCode: CodeMaxQty,
		},
		{
			name:     "max qty disabled",
			mutate:   func(r *Request) { r.Qty = 1_000_000 },
			wantCode: CodeOK,
		},

		// Rule 5: max notional.
		{
			name:     "computed notional equals max",
			cfg:      Config{MaxNotional: 100000},
			wantCode: CodeOK,
		},
		{
			name:     "computed notional over max",
			cfg:      Config{MaxNotional: 99999},
			wantCode: CodeMaxNotional,
		},
		{
			name: "explicit notional over max",
			cfg:  Config{MaxNotional: 1000},
			mutate: func(r *Request) {
				r.Price = 1
				r.Qty = 1
				r.Notional = 1001
			},
			wantCode: CodeMaxNotional,
		},
		{
			name: "explicit notional overrides computed",
			cfg:  Config{MaxNotional: 1000},
			mutate: func(r *Request) {
				r.Price = 10000
				r.Qty = 100
				r.Notional = 500
			},
			wantCode: CodeOK,
		},
		{
			name: "non-positive notional falls back to price times qty",
			cfg:  Config{MaxNotional: 1000},
			mutate: func(r *Request) {
				r.Price = 10000
				r.Qty = 100
				r.Notional = -1
			},
			wantCode: CodeMaxNotional,
		},
		{
			name: "notional overflow is over max",
			cfg:  Config{MaxNotional: math.MaxInt64},
			mutate: func(r *Request) {
				r.Price = math.MaxInt64
				r.Qty = 2
			},
			wantCode: CodeMaxNotional,
		},
		{
			name: "max notional disabled",
			mutate: func(r *Request) {
				r.Price = math.MaxInt64
				r.Qty = 2
			},
			wantCode: CodeOK,
		},

		// Rule 6: price band.
		{
			name: "price band upper boundary",
			cfg:  Config{PriceBandBps: 100},
			mutate: func(r *Request) {
				r.ReferencePrice = 10000
				r.Price = 10100
			},
			wantCode: CodeOK,
		},
		{
			name: "price band upper exceeded",
			cfg:  Config{PriceBandBps: 100},
			mutate: func(r *Request) {
				r.ReferencePrice = 10000
				r.Price = 10101
			},
			wantCode: CodePriceBand,
		},
		{
			name: "price band lower boundary",
			cfg:  Config{PriceBandBps: 100},
			mutate: func(r *Request) {
				r.ReferencePrice = 10000
				r.Price = 9900
			},
			wantCode: CodeOK,
		},
		{
			name: "price band lower exceeded",
			cfg:  Config{PriceBandBps: 100},
			mutate: func(r *Request) {
				r.ReferencePrice = 10000
				r.Price = 9899
			},
			wantCode: CodePriceBand,
		},
		{
			name: "price band without reference price",
			cfg:  Config{PriceBandBps: 100},
			mutate: func(r *Request) {
				r.ReferencePrice = 0
				r.Price = 1_000_000
			},
			wantCode: CodeOK,
		},
		{
			name: "price band disabled by zero bps",
			cfg:  Config{PriceBandBps: 0},
			mutate: func(r *Request) {
				r.ReferencePrice = 10000
				r.Price = 1_000_000
			},
			wantCode: CodeOK,
		},

		// Rule 7: position limit.
		{
			name: "position buy boundary",
			cfg:  Config{MaxPosition: 100},
			mutate: func(r *Request) {
				r.CurrentPosition = 95
				r.Qty = 5
			},
			wantCode: CodeOK,
		},
		{
			name: "position buy exceeded",
			cfg:  Config{MaxPosition: 100},
			mutate: func(r *Request) {
				r.CurrentPosition = 96
				r.Qty = 5
			},
			wantCode: CodeMaxPosition,
		},
		{
			name: "position sell boundary",
			cfg:  Config{MaxPosition: 100},
			mutate: func(r *Request) {
				r.CurrentPosition = -95
				r.Side = engine.SideSell
				r.Qty = 5
			},
			wantCode: CodeOK,
		},
		{
			name: "position sell exceeded",
			cfg:  Config{MaxPosition: 100},
			mutate: func(r *Request) {
				r.CurrentPosition = -96
				r.Side = engine.SideSell
				r.Qty = 5
			},
			wantCode: CodeMaxPosition,
		},
		{
			name: "position flip within limit",
			cfg:  Config{MaxPosition: 100},
			mutate: func(r *Request) {
				r.CurrentPosition = 5
				r.Side = engine.SideSell
				r.Qty = 10
			},
			wantCode: CodeOK,
		},
		{
			name: "position overflow",
			cfg:  Config{MaxPosition: math.MaxInt64},
			mutate: func(r *Request) {
				r.CurrentPosition = math.MaxInt64
				r.Qty = 1
			},
			wantCode: CodeMaxPosition,
		},
		{
			name: "position limit disabled",
			mutate: func(r *Request) {
				r.CurrentPosition = 1_000_000
				r.Qty = 1_000_000
			},
			wantCode: CodeOK,
		},

		// Rule 8: order rate limit.
		{
			name:     "rate limit allows up to max",
			cfg:      Config{MaxOrdersPerWindow: 2, Window: time.Minute},
			preCalls: 1,
			wantCode: CodeOK,
		},
		{
			name:     "rate limit rejects over max",
			cfg:      Config{MaxOrdersPerWindow: 2, Window: time.Minute},
			preCalls: 2,
			wantCode: CodeRateLimit,
		},
		{
			name:     "rate limit disabled by zero max",
			cfg:      Config{MaxOrdersPerWindow: 0, Window: time.Minute},
			preCalls: 3,
			wantCode: CodeOK,
		},
		{
			name:     "rate limit disabled by zero window",
			cfg:      Config{MaxOrdersPerWindow: 1, Window: 0},
			preCalls: 3,
			wantCode: CodeOK,
		},
		{
			name:     "rate limit uses time.Now when Now is zero",
			cfg:      Config{MaxOrdersPerWindow: 1, Window: time.Hour},
			mutate:   func(r *Request) { r.Now = time.Time{} },
			preCalls: 1,
			wantCode: CodeRateLimit,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			g := NewGuard(tc.cfg)
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			for i := 0; i < tc.preCalls; i++ {
				if got := g.Check(ctx, req); !got.Approved {
					t.Fatalf("pre-call %d rejected: %+v", i, got)
				}
			}

			got := g.Check(ctx, req)
			if got.Code != tc.wantCode {
				t.Fatalf("code = %s, want %s (message: %s)", got.Code, tc.wantCode, got.Message)
			}
			wantApproved := tc.wantCode == CodeOK
			if got.Approved != wantApproved {
				t.Fatalf("approved = %v, want %v", got.Approved, wantApproved)
			}
			if got.Approved && got.Message != "" {
				t.Fatalf("approved decision has message %q", got.Message)
			}
			if !got.Approved && got.Message == "" {
				t.Fatal("rejected decision has empty message")
			}
		})
	}
}

func TestGuardRateLimitReset(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	g := NewGuard(Config{MaxOrdersPerWindow: 2, Window: time.Minute})

	steps := []struct {
		offset time.Duration
		want   string
	}{
		{0, CodeOK},
		{0, CodeOK},
		{0, CodeRateLimit},
		{30 * time.Second, CodeRateLimit},
		{time.Minute, CodeOK},
		{time.Minute, CodeOK},
		{time.Minute, CodeRateLimit},
	}
	for i, step := range steps {
		req := baseRequest()
		req.Now = t0.Add(step.offset)
		got := g.Check(context.Background(), req)
		if got.Code != step.want {
			t.Fatalf("step %d at %s: code = %s, want %s", i, step.offset, got.Code, step.want)
		}
	}
}

func TestGuardRateLimitRejectedAttemptsDoNotCount(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	g := NewGuard(Config{MaxOrdersPerWindow: 1, Window: time.Minute})

	steps := []struct {
		offset time.Duration
		want   string
	}{
		{0, CodeOK},
		{30 * time.Second, CodeRateLimit},
		{time.Minute, CodeOK},
		{90 * time.Second, CodeRateLimit},
		{2 * time.Minute, CodeOK},
	}
	for i, step := range steps {
		req := baseRequest()
		req.Now = t0.Add(step.offset)
		got := g.Check(context.Background(), req)
		if got.Code != step.want {
			t.Fatalf("step %d at %s: code = %s, want %s", i, step.offset, got.Code, step.want)
		}
	}
}

func TestGuardRateLimitPerKey(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	g := NewGuard(Config{MaxOrdersPerWindow: 1, Window: time.Minute})

	req := baseRequest()
	req.Now = t0
	if got := g.Check(context.Background(), req); got.Code != CodeOK {
		t.Fatalf("first request: %+v", got)
	}
	if got := g.Check(context.Background(), req); got.Code != CodeRateLimit {
		t.Fatalf("same key: code = %s, want %s", got.Code, CodeRateLimit)
	}

	otherAccount := req
	otherAccount.AccountID = 43
	if got := g.Check(context.Background(), otherAccount); got.Code != CodeOK {
		t.Fatalf("other account: %+v", got)
	}

	otherSymbol := req
	otherSymbol.Symbol = "ETH-USDT"
	if got := g.Check(context.Background(), otherSymbol); got.Code != CodeOK {
		t.Fatalf("other symbol: %+v", got)
	}

	both := req
	both.AccountID = 43
	both.Symbol = "ETH-USDT"
	if got := g.Check(context.Background(), both); got.Code != CodeOK {
		t.Fatalf("other account and symbol: %+v", got)
	}
}

func TestGuardRateLimiterPrunesExpiredState(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	g := NewGuard(Config{MaxOrdersPerWindow: 1, Window: time.Second})

	for i := 0; i < 50; i++ {
		req := baseRequest()
		req.Symbol = fmt.Sprintf("SYM-%02d", i)
		req.Now = t0
		if got := g.Check(context.Background(), req); got.Code != CodeOK {
			t.Fatalf("symbol %d: %+v", i, got)
		}
	}
	if got := orderKeyCount(g); got != 50 {
		t.Fatalf("tracked keys = %d, want 50", got)
	}

	req := baseRequest()
	req.Symbol = "SYM-NEW"
	req.Now = t0.Add(time.Second)
	if got := g.Check(context.Background(), req); got.Code != CodeOK {
		t.Fatalf("new symbol after window: %+v", got)
	}
	if got := orderKeyCount(g); got != 1 {
		t.Fatalf("tracked keys after sweep = %d, want 1", got)
	}
}

func TestGuardConcurrentUse(t *testing.T) {
	g := NewGuard(Config{MaxOrdersPerWindow: 1000, Window: time.Hour})
	req := baseRequest()

	const workers = 64
	var wg sync.WaitGroup
	wg.Add(workers)
	decisions := make(chan Decision, workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			decisions <- g.Check(context.Background(), req)
		}()
	}
	wg.Wait()
	close(decisions)

	for got := range decisions {
		if !got.Approved || got.Code != CodeOK {
			t.Fatalf("concurrent check: %+v", got)
		}
	}
}

func TestGuardNilContext(t *testing.T) {
	g := NewGuard(Config{})
	var ctx context.Context
	if got := g.Check(ctx, baseRequest()); !got.Approved {
		t.Fatalf("nil context: %+v", got)
	}
}

func TestGuardCopiesWhitelist(t *testing.T) {
	cfg := Config{SymbolWhitelist: []string{"BTC-USDT"}}
	g := NewGuard(cfg)
	cfg.SymbolWhitelist[0] = "ETH-USDT"

	if got := g.Check(context.Background(), baseRequest()); !got.Approved {
		t.Fatalf("whitelist copy was mutated: %+v", got)
	}
}

func orderKeyCount(g *Guard) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.orders)
}
