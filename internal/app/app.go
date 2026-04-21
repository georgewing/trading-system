package app

import (
	"context"
	"log"
	"sync"

	"trading-system/internal/engine"
	"trading-system/internal/pipeline"
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
	ob := engine.NewOrderBook("BTCUSDT", 1, 1)
	matcher := engine.NewMatcher(ob)

	busCfg := pipeline.DefaultEventBusConfig()
	bus := pipeline.NewEventBusWithConfig(matcher, busCfg)
	bus.Start(ctx)

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
	worker.Start(ctx)

	log.Println("pipeline started: inbound matcher + outbound worker (ws/storage)")

	<-ctx.Done()
	log.Printf("pipeline stopped: persisted exec reports=%d", repo.Count())
	return nil
}
