// Command backtest replays a historical event file through a strategy and
// writes a performance report.
//
// It is a separate binary from hft-server on purpose: a backtest must never be
// able to affect a running matching process, and hft-server's startup contract
// stays unchanged.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"

	"go.yaml.in/yaml/v2"

	"trading-system/internal/backtest"
	"trading-system/internal/strategy"
)

type fileConfig struct {
	Backtest backtestConfig `yaml:"backtest"`
	Data     dataConfig     `yaml:"data"`
	Strategy strategyConfig `yaml:"strategy"`
	Report   reportConfig   `yaml:"report"`
}

type backtestConfig struct {
	InitialCash      int64  `yaml:"initial_cash"`
	MakerFeeBps      int64  `yaml:"maker_fee_bps"`
	TakerFeeBps      int64  `yaml:"taker_fee_bps"`
	SlippageBps      int64  `yaml:"slippage_bps"`
	ParticipationBps int64  `yaml:"participation_bps"`
	QueuePositionBps int64  `yaml:"queue_position_bps"`
	MaxLevels        int    `yaml:"max_levels"`
	Seed             uint64 `yaml:"seed"`
	BarSeconds       int64  `yaml:"bar_seconds"`
	EventTimerMillis int64  `yaml:"event_timer_millis"`

	MaxOrderRate  int `yaml:"max_order_rate"`
	MaxOpenOrders int `yaml:"max_open_orders"`
	MaxPanics     int `yaml:"max_panics"`
}

type dataConfig struct {
	Path string `yaml:"path"`
}

type strategyConfig struct {
	Name   string            `yaml:"name"`
	Params map[string]string `yaml:"params"`
}

type reportConfig struct {
	Path     string `yaml:"path"`
	Markdown string `yaml:"markdown"`
}

func main() {
	configPath := flag.String("config", "./configs/backtest.example.yaml", "backtest config file")
	outPath := flag.String("out", "", "override report JSON path")
	flag.Parse()

	if err := run(*configPath, *outPath); err != nil {
		log.Printf("backtest failed: %v", err)
		os.Exit(1)
	}
}

func run(configPath, outOverride string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	src, err := backtest.OpenJSONL(cfg.Data.Path)
	if err != nil {
		return fmt.Errorf("open data %s: %w", cfg.Data.Path, err)
	}
	defer src.Close()

	strat, err := buildStrategy(cfg.Strategy.Name)
	if err != nil {
		return err
	}

	sim := backtest.NewSimulator(backtest.Config{
		InitialCash: cfg.Backtest.InitialCash,
		Fee: backtest.FeeModel{
			MakerBps: cfg.Backtest.MakerFeeBps,
			TakerBps: cfg.Backtest.TakerFeeBps,
		},
		SlippageBps: cfg.Backtest.SlippageBps,
		Fill: backtest.FillModel{
			ParticipationBps: cfg.Backtest.ParticipationBps,
			QueuePositionBps: cfg.Backtest.QueuePositionBps,
		},
		MaxLevels: cfg.Backtest.MaxLevels,
		Seed:      cfg.Backtest.Seed,
	})

	runner := strategy.NewRunner(strat, sim, strategy.RunnerConfig{
		MaxOrderRate:  cfg.Backtest.MaxOrderRate,
		MaxOpenOrders: cfg.Backtest.MaxOpenOrders,
		MaxPanics:     cfg.Backtest.MaxPanics,
	})
	if err := runner.Init(cfg.Strategy.Params); err != nil {
		return fmt.Errorf("init strategy %s: %w", cfg.Strategy.Name, err)
	}
	defer func() {
		if err := runner.Stop(); err != nil {
			log.Printf("strategy stop: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	events, symbols, err := replay(ctx, src, sim, runner, cfg.Backtest.EventTimerMillis)
	if err != nil {
		return err
	}

	barMillis := cfg.Backtest.BarSeconds * 1000
	report := backtest.BuildReport(backtest.ReportInput{
		Strategy:  strat.Name(),
		Symbols:   symbols,
		Events:    events,
		BarMillis: barMillis,
		Portfolio: sim.Portfolio(),
		Stats:     sim.Stats(),
		Equity:    sim.Equity(),
	})

	jsonPath := cfg.Report.Path
	if outOverride != "" {
		jsonPath = outOverride
	}
	if jsonPath != "" {
		if err := writeJSON(jsonPath, report); err != nil {
			return err
		}
		log.Printf("report written: %s", jsonPath)
	}
	if cfg.Report.Markdown != "" {
		if err := writeMarkdown(cfg.Report.Markdown, report); err != nil {
			return err
		}
		log.Printf("markdown written: %s", cfg.Report.Markdown)
	}

	log.Printf("backtest done: strategy=%s events=%d final_equity=%d realized=%d fees=%d orders=%d fills=%d",
		report.Strategy, report.Events, report.FinalEquity, report.RealizedPnL, report.Fees,
		report.OrdersSubmitted, report.Fills)
	return nil
}

// replay drives the simulator and the strategy from one monotonic event stream.
// Market state is applied before the strategy callback so a strategy always
// observes the consequence of the event it is reacting to.
func replay(ctx context.Context, src backtest.Source, sim *backtest.Simulator, runner *strategy.Runner, timerMillis int64) (int, []string, error) {
	if timerMillis <= 0 {
		timerMillis = 60_000
	}

	symbols := make(map[string]struct{})
	var events int
	var lastTimer int64

	var ev backtest.Event
	for {
		ok, err := src.Next(ctx, &ev)
		if err != nil {
			return events, sortedSymbols(symbols), err
		}
		if !ok {
			break
		}
		events++
		symbols[ev.Symbol] = struct{}{}

		switch ev.Kind {
		case backtest.KindBook:
			if ev.Book == nil {
				continue
			}
			sim.ApplyBook(ev.Symbol, ev.Book.Bids, ev.Book.Asks, ev.TS)
			if view, ok := sim.Book(ev.Symbol); ok {
				if err := runner.OnBook(view); err != nil {
					log.Printf("strategy on_book: %v", err)
				}
			}

		case backtest.KindTrade:
			if ev.Trade == nil {
				continue
			}
			view := strategy.TradeView{
				Symbol: ev.Symbol,
				Price:  ev.Trade.Price,
				Qty:    ev.Trade.Qty,
				TS:     ev.TS,
			}
			sim.ApplyTrade(view)
			if err := runner.OnTrade(view); err != nil {
				log.Printf("strategy on_trade: %v", err)
			}
		}

		if ev.TS-lastTimer >= timerMillis {
			lastTimer = ev.TS
			if err := runner.OnTimer(ev.TS); err != nil {
				log.Printf("strategy on_timer: %v", err)
			}
		}
	}

	if err := runner.OnTimer(sim.Now()); err != nil {
		log.Printf("strategy final timer: %v", err)
	}
	return events, sortedSymbols(symbols), nil
}

func buildStrategy(name string) (strategy.Strategy, error) {
	switch name {
	case "", "noop":
		return strategy.NewNoop(), nil
	case "market_maker":
		return strategy.NewMarketMaker(), nil
	case "stat_arb":
		return strategy.NewStatArb(), nil
	default:
		return nil, fmt.Errorf("unknown strategy %q (want noop, market_maker or stat_arb)", name)
	}
}

func loadConfig(path string) (fileConfig, error) {
	var cfg fileConfig

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Data.Path == "" {
		return cfg, fmt.Errorf("parse %s: data.path is required", path)
	}
	if cfg.Backtest.BarSeconds <= 0 {
		cfg.Backtest.BarSeconds = 60
	}
	if cfg.Backtest.MaxLevels <= 0 {
		cfg.Backtest.MaxLevels = 10
	}
	if cfg.Backtest.Seed == 0 {
		cfg.Backtest.Seed = 1
	}
	return cfg, nil
}

func writeJSON(path string, report backtest.Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return report.WriteJSON(f)
}

func writeMarkdown(path string, report backtest.Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return report.WriteMarkdown(f)
}

func sortedSymbols(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
