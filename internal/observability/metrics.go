package observability

import "time"

// Recorder 定义撮合链路核心指标采集行为。
type Recorder interface {
	// IncOrderArrival 记录订单到达次数，result 推荐取值: accepted/rejected/canceled。
	IncOrderArrival(result string)
	// ObserveMatchLatency 记录撮合耗时分布，result 推荐取值: ok/error。
	ObserveMatchLatency(result string, d time.Duration)
	// SetQueueBacklog 记录队列积压，queue 推荐取值: inbound/outbound。
	SetQueueBacklog(queue string, n float64)
}

type noopRecorder struct{}

func (noopRecorder) IncOrderArrival(string) {}
func (noopRecorder) ObserveMatchLatency(string, time.Duration) {}
func (noopRecorder) SetQueueBacklog(string, float64) {}

var globalNoop Recorder = noopRecorder{}

// NoopRecorder 返回空实现，适用于未启用指标场景。
func NoopRecorder() Recorder {
	return globalNoop
}
