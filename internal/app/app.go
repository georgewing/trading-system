package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
	"trading-system/pkg/idgen"

	"trading-system/internal/execution"
	"trading-system/internal/hft/domain"
	"trading-system/internal/hft/engine"
	"trading-system/internal/hft/infra/binance"
	"trading-system/internal/hft/infra/memstore"
	"trading-system/internal/hft/infra/orders"
	"trading-system/internal/hft/risk"
	"trading-system/internal/observability"
	"trading-system/internal/pipeline"
	"trading-system/internal/routing"
	tradinghttp "trading-system/internal/transport/http"
	"trading-system/internal/transport/ws"
)

func Run(ctx context.Context, cfg *Config) error {
	v := os.Getenv("SNOWFLAKE_WORKER_ID")
	if v == "" {
		return errors.New("SNOWFLAKE_WORKER_ID is required")
	}
	workerID, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return err
	}
	if err := idgen.Init(workerID); err != nil {
		return err
	}

	if cfg.Matcher.Symbol == "" {
		return errors.New("matcher.symbol is required")
	}
	priceScale, err := pow10(cfg.Matcher.PricePrecision)
	if err != nil {
		return err
	}
	quantityScale, err := pow10(cfg.Matcher.QuantityPrecision)
	if err != nil {
		return err
	}

	metrics, err := observability.NewPrometheusMetrics(observability.DefaultPrometheusConfig())
	if err != nil {
		return err
	}

	apiLn, metricsLn, err := listenServers(cfg)
	if err != nil {
		return err
	}

	plane, err := newMarketPlane(cfg, priceScale, quantityScale)
	if err != nil {
		_ = apiLn.Close()
		_ = metricsLn.Close()
		return err
	}

	ob := engine.NewOrderBook(cfg.Matcher.Symbol, priceScale, quantityScale)
	matcher := engine.NewMatcher(ob)

	paper := execution.NewPaperVenue("paper", matcher)
	defaultVenue := cfg.Routing.DefaultVenue
	if defaultVenue == "" {
		defaultVenue = "paper"
	}
	router, err := routing.NewRouter(defaultVenue, map[string]execution.Venue{"paper": paper}, cfg.Routing.SymbolVenues)
	if err != nil {
		_ = apiLn.Close()
		_ = metricsLn.Close()
		return err
	}

	persistenceEnabled := cfg.Storage.PostgresEnabled || cfg.Storage.ClickHouseEnabled

	busCfg := pipeline.EventBusConfig{
		InboundCapacity:  cfg.Pipeline.InboundCapacity,
		OutboundCapacity: cfg.Pipeline.OutboundCapacity,
	}
	if persistenceEnabled {
		busCfg.ReportEnqueueTimeout = 50 * time.Millisecond
	}
	bus := pipeline.NewEventBusWithConfig(matcher, busCfg)
	bus.SetMetrics(metrics)
	bus.SetVenueResolver(router)

	extraSinks, pgWriter, err := newPersistenceSinks(ctx, cfg)
	if err != nil {
		_ = apiLn.Close()
		_ = metricsLn.Close()
		return err
	}

	// The position ledger exists even when risk checks are disabled, so the
	// portfolio endpoints always have a consistent (empty) answer instead of
	// disappearing from the API surface.
	positions := risk.NewPositions()

	var riskHub *ws.RiskHub
	var riskSink *risk.AsyncSink
	if cfg.Risk.Enabled {
		maxOrderQty, err := scaleBy(cfg.Risk.MaxOrderQty, quantityScale)
		if err != nil {
			closePGWriter(pgWriter)
			_ = apiLn.Close()
			_ = metricsLn.Close()
			return fmt.Errorf("risk.max_order_qty: %w", err)
		}
		maxPosition, err := scaleBy(cfg.Risk.MaxPosition, quantityScale)
		if err != nil {
			closePGWriter(pgWriter)
			_ = apiLn.Close()
			_ = metricsLn.Close()
			return fmt.Errorf("risk.max_position: %w", err)
		}
		maxDailyLoss, err := scaleNotional(cfg.Risk.Intraday.MaxDailyLoss, priceScale, quantityScale)
		if err != nil {
			closePGWriter(pgWriter)
			_ = apiLn.Close()
			_ = metricsLn.Close()
			return fmt.Errorf("risk.intraday.max_daily_loss: %w", err)
		}

		positions = risk.NewPositions()
		riskHub = ws.NewRiskHub(cfg.WebSocket.BufferSize, 100)
		riskSink = risk.NewAsyncSink(risk.MultiSink{risk.NewLogSink(), riskHub}, 1024, metrics)

		intraday := cfg.Risk.Intraday
		riskEngine := risk.NewEngine(risk.EngineConfig{
			PreTrade: risk.Config{
				SymbolWhitelist:    cfg.Risk.SymbolWhitelist,
				MaxOrderQty:        maxOrderQty,
				MaxNotional:        cfg.Risk.MaxNotional,
				PriceBandBps:       cfg.Risk.PriceBandBps,
				MaxOrdersPerWindow: cfg.Risk.MaxOrdersPerWindow,
				Window:             time.Duration(cfg.Risk.WindowSeconds) * time.Second,
				MaxPosition:        maxPosition,
			},
			Intraday: risk.IntradayConfig{
				MaxCancelRatioBps: intraday.MaxCancelRatioBps,
				CancelWindow:      time.Duration(intraday.CancelWindowSeconds) * time.Second,
				MinCancelSamples:  intraday.MinCancelSamples,
				MaxOpenOrders:     intraday.MaxOpenOrders,
				MaxDailyLoss:      maxDailyLoss,
				MaxDrawdownBps:    intraday.MaxDrawdownBps,
				Cooldown:          time.Duration(intraday.CooldownSeconds) * time.Second,
				ResumeAuto:        intraday.ResumeAuto,
			},
		}, positions,
			risk.WithReferencePrice(referencePrice(plane.snapshotter())),
			risk.WithEvents(riskSink),
			risk.WithMetrics(metrics),
		)
		bus.SetRiskGuard(riskEngine, quantityScale)
		bus.SetRiskPriceScale(priceScale)
		bus.SetRiskObserver(riskEngine)
		log.Printf("risk enabled: whitelist=%d max_order_qty_lots=%d max_notional_quote=%d price_band_bps=%d max_orders_per_window=%d window=%ds max_position_lots=%d",
			len(cfg.Risk.SymbolWhitelist), maxOrderQty, cfg.Risk.MaxNotional, cfg.Risk.PriceBandBps,
			cfg.Risk.MaxOrdersPerWindow, cfg.Risk.WindowSeconds, maxPosition)
		log.Printf("intraday risk: cancel_ratio_bps=%d cancel_window=%ds min_samples=%d max_open_orders=%d max_daily_loss=%d max_drawdown_bps=%d cooldown=%ds resume_auto=%v",
			intraday.MaxCancelRatioBps, intraday.CancelWindowSeconds, intraday.MinCancelSamples,
			intraday.MaxOpenOrders, intraday.MaxDailyLoss, intraday.MaxDrawdownBps,
			intraday.CooldownSeconds, intraday.ResumeAuto)
	}

	pipelineCtx, pipelineCancel := context.WithCancel(context.Background())

	if plane.source != nil {
		emit := func(ev domain.Event) {
			plane.cache.Apply(ev)
			plane.fanout.Publish(ev)

			stream := ev.Kind.String()
			metrics.IncFeedMessage(stream)
			if ev.TS > 0 {
				lag := time.Since(time.UnixMilli(ev.TS))
				if lag < 0 {
					lag = 0
				}
				metrics.SetFeedLag(stream, lag)
			}
		}

		go func() {
			if err := plane.source.Run(pipelineCtx, plane.symbols, emit); err != nil && pipelineCtx.Err() == nil {
				log.Printf("market feed stopped: %v", err)
			}
		}()
		go reportFeedCounters(pipelineCtx, plane, metrics)

		log.Printf("market feed started: provider=%s symbols=%v depth=%d@%s kline=%q",
			cfg.Market.Provider, plane.symbols, cfg.Market.DepthLevels, cfg.Market.DepthSpeed, cfg.Market.KlineInterval)
	}

	bus.Start(pipelineCtx)

	wsHub := ws.NewHub(cfg.WebSocket.BufferSize)
	wsHub.SetMetrics(metrics)
	book := orders.NewBook(10000)

	workerCfg := pipeline.DefaultOutboundWorkerConfig()
	if persistenceEnabled {
		workerCfg.DispatchTimeout = 50 * time.Millisecond
		workerCfg.SinkQueueSize = 16384
	}
	sinks := []pipeline.ExecReportSink{
		pipeline.NewWSSink(wsHub),
		pipeline.NewStorageSink(book),
	}
	sinks = append(sinks, extraSinks...)
	worker := pipeline.NewOutboundWorker(
		bus.Outbound(),
		workerCfg,
		&pipeline.LogDeadLetter{},
		sinks...,
	)
	worker.SetMetrics(metrics)
	worker.Start(pipelineCtx)

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())
	metricsSrv := &http.Server{
		Addr:              cfg.Server.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	apiMux := http.NewServeMux()
	apiMux.Handle("/api/v1/order", tradinghttp.MakeMatchEndpoint(
		bus,
		cfg.Matcher.Symbol,
		tradinghttp.WithOrderObserver(book.AddOrder),
	))
	apiMux.Handle("/api/v1/orders", tradinghttp.MakeOrdersEndpoint(book))
	apiMux.Handle("/api/v1/orders/", tradinghttp.MakeOrdersEndpoint(book))
	apiMux.Handle("/api/v1/meta", tradinghttp.MakeMetaEndpoint(
		cfg.Matcher.Symbol, cfg.Matcher.PricePrecision, cfg.Matcher.QuantityPrecision,
	))
	apiMux.Handle("/api/v1/positions", tradinghttp.MakePositionsEndpoint(
		positions, cfg.Risk.Enabled, cfg.Matcher.PricePrecision, cfg.Matcher.QuantityPrecision,
	))
	apiMux.Handle("/api/v1/account", tradinghttp.MakeAccountEndpoint(
		positions, cfg.Risk.Enabled, cfg.Matcher.PricePrecision, cfg.Matcher.QuantityPrecision,
	))
	apiMux.Handle("/api/v1/order/cancel", tradinghttp.MakeCancelEndpoint(bus))

	snapshotter := plane.snapshotter()
	apiMux.Handle("/api/v1/market/depth", tradinghttp.MakeDepthEndpoint(
		snapshotter, cfg.Matcher.Symbol, cfg.Matcher.PricePrecision, cfg.Matcher.QuantityPrecision,
	))
	apiMux.Handle("/api/v1/market/trades", tradinghttp.MakeTradesEndpoint(
		snapshotter, cfg.Matcher.Symbol, cfg.Matcher.PricePrecision, cfg.Matcher.QuantityPrecision,
	))
	apiMux.Handle("/api/v1/market/kline", tradinghttp.MakeKlineEndpoint(
		snapshotter, cfg.Matcher.Symbol, cfg.Matcher.PricePrecision, cfg.Matcher.QuantityPrecision,
	))

	wsServer := ws.NewServer(plane.stream(), snapshotter, wsHub, ws.ServerConfig{
		Symbol:            cfg.Matcher.Symbol,
		PricePrecision:    cfg.Matcher.PricePrecision,
		QuantityPrecision: cfg.Matcher.QuantityPrecision,
	})
	wsServer.SetMetrics(metrics)
	if riskHub != nil {
		wsServer.SetRiskFeed(riskHub)
	}
	apiMux.Handle("/ws", wsServer)

	apiSrv := &http.Server{
		Addr:              cfg.Server.APIAddr,
		Handler:           apiMux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	log.Println("pipeline started: inbound matcher + outbound worker (ws/storage)")

	runErr := serveServers(ctx, apiSrv, metricsSrv, apiLn, metricsLn)

	bus.StopAccepting()

	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := bus.Drain(drainCtx); err != nil {
		log.Printf("eventbus drain failed: %v", err)
	}
	if err := worker.Stop(drainCtx); err != nil {
		log.Printf("outbound worker drain failed: %v", err)
	}
	drainCancel()

	pipelineCancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := worker.Wait(waitCtx); err != nil {
		log.Printf("outbound worker wait failed: %v", err)
	}
	waitCancel()

	if pgWriter != nil {
		if err := pgWriter.Close(); err != nil {
			log.Printf("postgres writer close failed: %v", err)
		}
	}

	if riskSink != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := riskSink.Close(closeCtx); err != nil {
			log.Printf("risk sink close failed: %v", err)
		}
		closeCancel()
	}

	log.Printf("pipeline stopped: tracked orders=%d", book.Len())
	return runErr
}

// marketPlane groups the market-data adapters. A zero-value plane (market
// disabled) is valid: both accessors return nil and the transports degrade to
// "market data unavailable" instead of failing startup.
type marketPlane struct {
	fanout  *domain.MemFanout
	cache   *memstore.Market
	symbols []string
	source  *binance.Source
}

func (p *marketPlane) stream() domain.Fanout {
	if p == nil || p.fanout == nil {
		return nil
	}
	return p.fanout
}

func (p *marketPlane) snapshotter() domain.Snapshotter {
	if p == nil || p.cache == nil {
		return nil
	}
	return p.cache
}

// newMarketPlane wires the market-data ports: a Binance source feeding both the
// snapshot cache and the fan-out.
func newMarketPlane(cfg *Config, priceScale, qtyScale int64) (*marketPlane, error) {
	if !cfg.Market.Enabled {
		return &marketPlane{}, nil
	}

	provider := cfg.Market.Provider
	if provider == "" {
		provider = "binance"
	}
	if provider != "binance" {
		return nil, fmt.Errorf("unsupported market provider %q", provider)
	}

	symbol := cfg.Market.Symbol
	if symbol == "" {
		symbol = cfg.Matcher.Symbol
	}

	dialer, err := binance.NewWebsocketDialer(binance.StreamConfig{
		BaseURL:       cfg.Market.WSURL,
		DepthLevels:   cfg.Market.DepthLevels,
		DepthSpeed:    cfg.Market.DepthSpeed,
		KlineInterval: cfg.Market.KlineInterval,
	})
	if err != nil {
		return nil, err
	}

	backoff := binance.Backoff{Base: time.Second, Max: 30 * time.Second}
	if cfg.Market.ReconnectMinSeconds > 0 {
		backoff.Base = time.Duration(cfg.Market.ReconnectMinSeconds) * time.Second
	}
	if cfg.Market.ReconnectMaxSeconds > 0 {
		backoff.Max = time.Duration(cfg.Market.ReconnectMaxSeconds) * time.Second
	}

	source, err := binance.NewSource(binance.SourceConfig{
		Decoder: binance.Decoder{
			PriceScale: priceScale,
			QtyScale:   qtyScale,
			Symbol:     symbol,
		},
		Dial:    dialer,
		Backoff: backoff,
	})
	if err != nil {
		return nil, err
	}

	return &marketPlane{
		fanout: domain.NewMemFanout(cfg.WebSocket.BufferSize),
		cache: memstore.New(memstore.Config{
			TradeCap: cfg.Market.StoreTradeCap,
			KlineCap: cfg.Market.StoreKlineCap,
		}),
		symbols: []string{symbol},
		source:  source,
	}, nil
}

// reportFeedCounters mirrors the source's internal counters into Prometheus.
// The counters are cumulative, so only the delta since the last tick is added.
func reportFeedCounters(ctx context.Context, plane *marketPlane, met *observability.PrometheusMetrics) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var reconnects, drops uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := plane.source.Reconnects(); n > reconnects {
				met.AddFeedReconnect("binance", n-reconnects)
				reconnects = n
			}
			if n := plane.fanout.Dropped(); n > drops {
				met.AddFeedDrop("fanout", n-drops)
				drops = n
			}
		}
	}
}

// referencePrice builds the mid-price provider used by the price-band check. It
// prefers the live market book and falls back to "no reference" (0) when data
// is unavailable, which disables the check instead of rejecting every order.
// Depth levels are already integers in the instrument's minimal unit, so no
// string parsing or float conversion is involved.
func referencePrice(snap domain.Snapshotter) func(string) int64 {
	if snap == nil {
		return nil
	}
	return func(symbol string) int64 {
		depth, ok := snap.Depth(symbol)
		if !ok || len(depth.Bids) == 0 || len(depth.Asks) == 0 {
			return 0
		}
		bid := depth.Bids[0].Price
		ask := depth.Asks[0].Price
		if bid <= 0 || ask <= 0 || ask < bid {
			return 0
		}
		return bid + (ask-bid)/2
	}
}

func pow10(exp int) (int64, error) {
	if exp < 0 || exp > 18 {
		return 0, fmt.Errorf("invalid precision exponent %d", exp)
	}
	v := int64(1)
	for i := 0; i < exp; i++ {
		if v > math.MaxInt64/10 {
			return 0, fmt.Errorf("precision exponent %d overflows", exp)
		}
		v *= 10
	}
	return v, nil
}

// scaleBy converts a human-readable limit into engine integer units. A zero or
// negative limit is passed through so the risk config stays disabled.
func scaleBy(v, scale int64) (int64, error) {
	if v <= 0 {
		return v, nil
	}
	if scale <= 0 {
		return 0, fmt.Errorf("scale must be positive")
	}
	if v > math.MaxInt64/scale {
		return 0, fmt.Errorf("value %d overflows with scale %d", v, scale)
	}
	return v * scale, nil
}

func scaleNotional(v, priceScale, quantityScale int64) (int64, error) {
	if v <= 0 {
		return v, nil
	}
	if priceScale <= 0 || quantityScale <= 0 {
		return 0, fmt.Errorf("scales must be positive")
	}
	if v > math.MaxInt64/priceScale {
		return 0, fmt.Errorf("value %d overflows with price scale %d", v, priceScale)
	}
	v *= priceScale
	if v > math.MaxInt64/quantityScale {
		return 0, fmt.Errorf("value %d overflows with quantity scale %d", v, quantityScale)
	}
	return v * quantityScale, nil
}

// listenServers binds both HTTP listeners before any serving starts. This makes
// bind failures synchronous and prevents a half-started process.
func listenServers(cfg *Config) (net.Listener, net.Listener, error) {
	apiLn, err := net.Listen("tcp", cfg.Server.APIAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen api %s: %w", cfg.Server.APIAddr, err)
	}

	metricsLn, err := net.Listen("tcp", cfg.Server.MetricsAddr)
	if err != nil {
		_ = apiLn.Close()
		return nil, nil, fmt.Errorf("listen metrics %s: %w", cfg.Server.MetricsAddr, err)
	}

	return apiLn, metricsLn, nil
}

// serveServers runs both HTTP servers and returns the first unexpected serve
// error. Normal Shutdown results (http.ErrServerClosed) are ignored.
func serveServers(ctx context.Context, apiSrv, metricsSrv *http.Server, apiLn, metricsLn net.Listener) error {
	var wg sync.WaitGroup
	serveErrCh := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		log.Printf("trading api listening on %s", apiLn.Addr())
		if err := apiSrv.Serve(apiLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- fmt.Errorf("api serve: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		log.Printf("metrics listening on %s", metricsLn.Addr())
		if err := metricsSrv.Serve(metricsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- fmt.Errorf("metrics serve: %w", err)
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-serveErrCh:
		log.Printf("server stopped with error: %v", runErr)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := apiSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("api shutdown failed: %v", err)
	}
	shutdownCancel()

	shutdownCtx, shutdownCancel = context.WithTimeout(context.Background(), 3*time.Second)
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("metrics shutdown failed: %v", err)
	}
	shutdownCancel()

	wg.Wait()
	return runErr
}
