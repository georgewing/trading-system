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

	"trading-system/internal/engine"
	"trading-system/internal/gateway/binance"
	"trading-system/internal/market"
	"trading-system/internal/observability"
	"trading-system/internal/orders"
	"trading-system/internal/pipeline"
	"trading-system/internal/risk"
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

	marketStore, marketClient, err := newMarketFeed(cfg, metrics)
	if err != nil {
		_ = apiLn.Close()
		_ = metricsLn.Close()
		return err
	}

	ob := engine.NewOrderBook(cfg.Matcher.Symbol, priceScale, quantityScale)
	matcher := engine.NewMatcher(ob)

	busCfg := pipeline.EventBusConfig{
		InboundCapacity:  cfg.Pipeline.InboundCapacity,
		OutboundCapacity: cfg.Pipeline.OutboundCapacity,
	}
	bus := pipeline.NewEventBusWithConfig(matcher, busCfg)
	bus.SetMetrics(metrics)
	if cfg.Risk.Enabled {
		bus.SetRiskGuard(risk.NewGuard(risk.Config{
			SymbolWhitelist:    cfg.Risk.SymbolWhitelist,
			MaxOrderQty:        cfg.Risk.MaxOrderQty,
			MaxNotional:        cfg.Risk.MaxNotional,
			PriceBandBps:       cfg.Risk.PriceBandBps,
			MaxOrdersPerWindow: cfg.Risk.MaxOrdersPerWindow,
			Window:             time.Duration(cfg.Risk.WindowSeconds) * time.Second,
			MaxPosition:        cfg.Risk.MaxPosition,
		}), quantityScale)
		log.Printf("pre-trade risk enabled: whitelist=%d max_order_qty=%d max_notional=%d price_band_bps=%d max_orders_per_window=%d window=%ds max_position=%d",
			len(cfg.Risk.SymbolWhitelist), cfg.Risk.MaxOrderQty, cfg.Risk.MaxNotional, cfg.Risk.PriceBandBps,
			cfg.Risk.MaxOrdersPerWindow, cfg.Risk.WindowSeconds, cfg.Risk.MaxPosition)
	}
	pipelineCtx, pipelineCancel := context.WithCancel(context.Background())

	if marketClient != nil {
		go func() {
			if err := marketClient.Run(pipelineCtx); err != nil && pipelineCtx.Err() == nil {
				log.Printf("market feed stopped: %v", err)
			}
		}()
		log.Printf("market feed started: provider=%s stream=%s", cfg.Market.Provider, marketClient.URL())
	}

	bus.Start(pipelineCtx)

	wsHub := ws.NewHub(cfg.WebSocket.BufferSize)
	wsHub.SetMetrics(metrics)
	book := orders.NewBook(10000)

	workerCfg := pipeline.DefaultOutboundWorkerConfig()
	worker := pipeline.NewOutboundWorker(
		bus.Outbound(),
		workerCfg,
		&pipeline.LogDeadLetter{},
		pipeline.NewWSSink(wsHub),
		pipeline.NewStorageSink(book),
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

	var marketFeed ws.MarketFeed
	if marketStore != nil {
		marketFeed = marketStore
		apiMux.Handle("/api/v1/market/depth", tradinghttp.MakeDepthEndpoint(marketStore, cfg.Matcher.Symbol))
		apiMux.Handle("/api/v1/market/trades", tradinghttp.MakeTradesEndpoint(marketStore, cfg.Matcher.Symbol))
		apiMux.Handle("/api/v1/market/kline", tradinghttp.MakeKlineEndpoint(marketStore, cfg.Matcher.Symbol))
	}

	wsServer := ws.NewServer(marketFeed, wsHub, ws.ServerConfig{
		Symbol:            cfg.Matcher.Symbol,
		PricePrecision:    cfg.Matcher.PricePrecision,
		QuantityPrecision: cfg.Matcher.QuantityPrecision,
	})
	wsServer.SetMetrics(metrics)
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

	log.Printf("pipeline stopped: tracked orders=%d", book.Len())
	return runErr
}

// newMarketFeed builds the optional external market-data feed. It returns nils
// when the feed is disabled, so callers can treat a nil store as "market data
// unavailable" without branching on config.
func newMarketFeed(cfg *Config, met *observability.PrometheusMetrics) (*market.Store, *binance.Client, error) {
	if !cfg.Market.Enabled {
		return nil, nil, nil
	}

	provider := cfg.Market.Provider
	if provider == "" {
		provider = "binance"
	}
	if provider != "binance" {
		return nil, nil, fmt.Errorf("unsupported market provider %q", provider)
	}

	storeCfg := market.DefaultStoreConfig()
	if cfg.Market.StoreTradeCap > 0 {
		storeCfg.TradeCap = cfg.Market.StoreTradeCap
	}
	if cfg.Market.StoreKlineCap > 0 {
		storeCfg.KlineCap = cfg.Market.StoreKlineCap
	}

	store := market.NewStore(storeCfg)
	store.SetMetrics(met)

	feedCfg := binance.DefaultConfig()
	if cfg.Market.WSURL != "" {
		feedCfg.WSURL = cfg.Market.WSURL
	}
	feedCfg.Symbol = cfg.Market.Symbol
	if feedCfg.Symbol == "" {
		feedCfg.Symbol = cfg.Matcher.Symbol
	}
	if cfg.Market.DepthLevels > 0 {
		feedCfg.DepthLevels = cfg.Market.DepthLevels
	}
	if cfg.Market.DepthSpeed != "" {
		feedCfg.DepthSpeed = cfg.Market.DepthSpeed
	}
	if cfg.Market.KlineInterval != "" {
		feedCfg.KlineInterval = cfg.Market.KlineInterval
	}
	if cfg.Market.ReconnectMinSeconds > 0 {
		feedCfg.ReconnectMin = time.Duration(cfg.Market.ReconnectMinSeconds) * time.Second
	}
	if cfg.Market.ReconnectMaxSeconds > 0 {
		feedCfg.ReconnectMax = time.Duration(cfg.Market.ReconnectMaxSeconds) * time.Second
	}

	client, err := binance.NewClient(feedCfg, store, met)
	if err != nil {
		return nil, nil, err
	}
	return store, client, nil
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
