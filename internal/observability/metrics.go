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
	// IncQueueDrop 记录队列/派发丢弃，queue 推荐取值: inbound/outbound/sink 名称。
	IncQueueDrop(queue string)
	// IncSinkFailure 记录 sink 最终失败次数。
	IncSinkFailure(sink string)
	// IncWSDrop 记录慢 WS 消费者丢弃次数。
	IncWSDrop()
	// IncRiskDecision 记录事前风控裁决次数，code 为风控决策码（如 OK/MAX_QTY），
	// result 推荐取值: approved/rejected；空值由实现归一为 "unknown"。
	IncRiskDecision(code, result string)
}

type noopRecorder struct{}

func (noopRecorder) IncOrderArrival(string)                    {}
func (noopRecorder) ObserveMatchLatency(string, time.Duration) {}
func (noopRecorder) SetQueueBacklog(string, float64)           {}
func (noopRecorder) IncQueueDrop(string)                       {}
func (noopRecorder) IncSinkFailure(string)                     {}
func (noopRecorder) IncWSDrop()                                {}
func (noopRecorder) IncRiskDecision(string, string)            {}

var globalNoop Recorder = noopRecorder{}

// NoopRecorder 返回空实现，适用于未启用指标场景。
func NoopRecorder() Recorder {
	return globalNoop
}
