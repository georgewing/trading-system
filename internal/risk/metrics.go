package risk

// Metrics is the optional observability surface for the risk engine. It is
// separate from observability.Recorder so existing implementations do not need
// new methods just to keep compiling.
type Metrics interface {
	// IncRiskEvent records a risk event by code and action.
	IncRiskEvent(code, action string)
	// SetStrategyState sets the current state gauge for a strategy.
	SetStrategyState(strategy, state string)
	// IncRiskEventDrop records an event dropped by the async sink.
	IncRiskEventDrop()
}

type noopMetrics struct{}

func (noopMetrics) IncRiskEvent(string, string)     {}
func (noopMetrics) SetStrategyState(string, string) {}
func (noopMetrics) IncRiskEventDrop()               {}
