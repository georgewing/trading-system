package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
	"trading-system/pkg/idgen"

	"trading-system/internal/engine"
	"trading-system/internal/observability"
	"trading-system/internal/pipeline"
	tradinghttp "trading-system/internal/transport/http"
	"trading-system/internal/transport/ws"
)

type inMemoryExecReportRepo struct {
	mu      sync.Mutex
	reports []engine.ExecReport
}

func (r *inMemoryExecReportRepo) InsertExecReport(_ context.Context, report engine.ExecReport) error {
	r.mu.Lock()
	r.reports = append(r.reports, report)
	r.mu.Unlock()
	return nil
}

func (r *inMemoryExecReportRepo) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reports)
}

func Run(ctx context.Context) error {
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

	metrics, err := observability.NewPrometheusMetrics(observability.DefaultPrometheusConfig())
	if err != nil {
		return err
	}

	ob := engine.NewOrderBook("BTCUSDT", 1, 1)
	matcher := engine.NewMatcher(ob)

	busCfg := pipeline.DefaultEventBusConfig()
	bus := pipeline.NewEventBusWithConfig(matcher, busCfg)
	bus.SetMetrics(metrics)
	pipelineCtx, pipelineCancel := context.WithCancel(context.Background())
	bus.Start(pipelineCtx)

	wsHub := ws.NewHub(1024)
	repo := &inMemoryExecReportRepo{}

	workerCfg := pipeline.DefaultOutboundWorkerConfig()
	worker := pipeline.NewOutboundWorker(
		bus.Outbound(),
		workerCfg,
		&pipeline.LogDeadLetter{},
		pipeline.NewWSSink(wsHub),
		pipeline.NewStorageSink(repo),
	)
	worker.SetMetrics(metrics)
	worker.Start(pipelineCtx)

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())
	metricsSrv := &http.Server{
		Addr:              ":9090",
		Handler:           metricsMux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	apiMux := http.NewServeMux()
	apiMux.Handle("/api/v1/order", tradinghttp.MakeMatchEndpoint(bus))

	apiSrv := &http.Server{
		Addr:              ":8080",
		Handler:           apiMux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	go func() {
		log.Printf("Trading API server listening on :8080")
		if err := apiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("api server stopped with error: %v", err)
		}
	}()

	go func() {
		if serveErr := metricsSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("metrics server stopped with error: %v", serveErr)
		}
	}()
	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = apiSrv.Shutdown(shutdownCtx) // 停止 API 接收新订单
		defer cancel()

		pipelineCancel()

		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = worker.Wait(waitCtx)
		defer cancel()

		shutdownCtx2, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = metricsSrv.Shutdown(shutdownCtx2)
		defer cancel()
	}()

	log.Println("pipeline started: inbound matcher + outbound worker (ws/storage), metrics=:9090/metrics")

	<-ctx.Done()
	log.Printf("pipeline stopped: persisted exec reports=%d", repo.Count())
	return nil
}
