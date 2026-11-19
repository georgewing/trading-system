package app

import (
	"context"
	"fmt"
	"time"

	"trading-system/internal/hft/live"
	"trading-system/internal/hft/strategy"
)

// newLiveStrategy builds a strategy supported by the single-symbol gateway.
func newLiveStrategy(name string) (strategy.Strategy, error) {
	switch name {
	case "", "noop":
		return strategy.NewNoop(), nil
	case "market_maker":
		return strategy.NewMarketMaker(), nil
	case "stat_arb":
		return nil, fmt.Errorf("strategy.stat_arb needs two symbols and is backtest-only")
	default:
		return nil, fmt.Errorf("unknown live strategy %q (want noop or market_maker)", name)
	}
}

// startLiveStrategy validates the live strategy configuration and starts the
// runner. It returns (nil, nil) when the feature is disabled.
//
// Live strategies require risk to be enabled: the position ledger and the
// strategy-scoped breaker both come from the risk stage.
func startLiveStrategy(ctx context.Context, cfg *Config, deps live.Deps) (*live.Runner, error) {
	s := cfg.Strategy
	if !s.Enabled {
		return nil, nil
	}
	if !cfg.Risk.Enabled {
		return nil, fmt.Errorf("strategy.enabled requires risk.enabled")
	}
	if !cfg.Market.Enabled {
		return nil, fmt.Errorf("strategy.enabled requires market.enabled for order-book input")
	}
	if s.AccountID == 0 {
		return nil, fmt.Errorf("strategy.account_id is required")
	}

	symbol := s.Symbol
	if symbol == "" {
		symbol = cfg.Matcher.Symbol
	}
	if symbol != cfg.Matcher.Symbol {
		return nil, fmt.Errorf("strategy.symbol %q is not served; gateway matches %q", symbol, cfg.Matcher.Symbol)
	}

	strat, err := newLiveStrategy(s.Name)
	if err != nil {
		return nil, err
	}

	timer := time.Duration(s.TimerSeconds) * time.Second
	if timer <= 0 {
		timer = 5 * time.Second
	}
	submitTimeout := time.Duration(s.SubmitTimeoutMillis) * time.Millisecond
	if submitTimeout <= 0 {
		submitTimeout = 2 * time.Second
	}

	deps.AccountID = s.AccountID
	deps.Symbol = symbol
	deps.Timer = timer
	deps.SubmitTimeout = submitTimeout

	return live.Start(ctx, deps, live.Config{
		Strategy: strat,
		Params:   s.Params,
		Runner: strategy.RunnerConfig{
			MaxOrderRate:  s.MaxOrderRate,
			MaxOpenOrders: s.MaxOpenOrders,
			MaxPanics:     s.MaxPanics,
		},
	})
}
