package pipeline

import (
	"context"
	"log"
	"trading-system/internal/engine"
)

// ExecReportBroadcaster 由 WS 层实现，用于广播执行回报
type ExecReportBroadcaster interface {
	BroadcastExecReport(report engine.ExecReport) error
}

// ExecReportRepository 由存储层实现，用于持久化执行回报
type ExecReportRepository interface{}
