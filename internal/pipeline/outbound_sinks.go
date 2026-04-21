package pipeline

import (
	"context"
	"log"
	"trading-system/internal/engine"
)

type ExecReportBroadcaster interface {
	BroadcastExecReport(ctx context.Context, report engine.ExecReport) error
}

type ExecReportRepository interface {
	InsertExecReport(ctx context.Context, report engine.ExecReport) error
}

type WSSink struct {
	broadcaster ExecReportBroadcaster
}

func NewWSSink(b ExecReportBroadcaster) *WSSink { return &WSSink{broadcaster: b} }
func (s *WSSink) Name() string                  { return "ws" }

func (s *WSSink) HandleExecReport(ctx context.Context, report engine.ExecReport) error {
	if s == nil || s.broadcaster == nil {
		return nil
	}
	return s.broadcaster.BroadcastExecReport(ctx, report)
}

type StorageSink struct {
	repo ExecReportRepository
}

func NewStorageSink(repo ExecReportRepository) *StorageSink { return &StorageSink{repo: repo} }
func (s *StorageSink) Name() string                         { return "storage" }

func (s *StorageSink) HandleExecReport(ctx context.Context, report engine.ExecReport) error {
	if s == nil || s.repo == nil {
		return nil
	}
	return s.repo.InsertExecReport(ctx, report)
}

type LoggingSink struct{}

func NewLoggingSink() *LoggingSink  { return &LoggingSink{} }
func (s *LoggingSink) Name() string { return "log" }

func (s *LoggingSink) HandleExecReport(_ context.Context, report engine.ExecReport) error {
	if report.Trade != nil {
		log.Printf("exec_report orderID=%d status=%d filled=%d leaves=%d tradeID=%d",
			report.OrderID, report.Status, report.Filled, report.Leaves, report.Trade.ID)
		return nil
	}
	log.Printf("exec_report orderID=%d status=%d filled=%d leaves=%d reason=%s",
		report.OrderID, report.Status, report.Filled, report.Leaves, report.Reason)
	return nil
}
