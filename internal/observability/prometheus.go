package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type PrometheusConfig struct {
	Namespace           string
	Subsystem           string
	MatchLatencyBuckets []float64
}

func DefaultPrometheusConfig() PrometheusConfig {
	return PrometheusConfig{
		Namespace: "trading_system",
		Subsystem: "matching",
		MatchLatencyBuckets: []float64{
			0.00001, 0.00002, 0.00005, 0.0001,
			0.0002, 0.0005, 0.001, 0.002,
			0.005, 0.01, 0.02, 0.05,
			0.1, 0.2, 0.5, 1.0,
		},
	}
}

type PrometheusMetrics struct {
	registry *prometheus.Registry

	orderArrivalTotal *prometheus.CounterVec
	matchLatency      *prometheus.HistogramVec
	queueBacklog      *prometheus.GaugeVec
	queueDropped      *prometheus.CounterVec
	sinkFailures      *prometheus.CounterVec
	wsDropped         prometheus.Counter
	riskDecision      *prometheus.CounterVec

	feedMessages   *prometheus.CounterVec
	feedReconnects *prometheus.CounterVec
	feedLag        *prometheus.GaugeVec
	feedDropped    *prometheus.CounterVec
}

func NewPrometheusMetrics(cfg PrometheusConfig) (*PrometheusMetrics, error) {
	if cfg.Namespace == "" {
		cfg.Namespace = "trading_system"
	}
	if cfg.Subsystem == "" {
		cfg.Subsystem = "matching"
	}
	if len(cfg.MatchLatencyBuckets) == 0 {
		cfg.MatchLatencyBuckets = DefaultPrometheusConfig().MatchLatencyBuckets
	}

	registry := prometheus.NewRegistry()
	m := &PrometheusMetrics{
		registry: registry,
		orderArrivalTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: cfg.Namespace,
				Subsystem: cfg.Subsystem,
				Name:      "order_arrival_total",
				Help:      "Total number of order arrival attempts by result.",
			},
			[]string{"result"},
		),
		matchLatency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: cfg.Namespace,
				Subsystem: cfg.Subsystem,
				Name:      "match_latency_seconds",
				Help:      "Order matching latency distribution in seconds.",
				Buckets:   cfg.MatchLatencyBuckets,
			},
			[]string{"result"},
		),
		queueBacklog: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: cfg.Namespace,
				Subsystem: cfg.Subsystem,
				Name:      "queue_backlog",
				Help:      "Current queue backlog length.",
			},
			[]string{"queue"},
		),
		queueDropped: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: cfg.Namespace,
				Subsystem: cfg.Subsystem,
				Name:      "queue_dropped_total",
				Help:      "Total number of dropped queue items by queue.",
			},
			[]string{"queue"},
		),
		sinkFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: cfg.Namespace,
				Subsystem: cfg.Subsystem,
				Name:      "sink_failures_total",
				Help:      "Total number of sink failures after retries.",
			},
			[]string{"sink"},
		),
		wsDropped: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: cfg.Namespace,
				Subsystem: cfg.Subsystem,
				Name:      "ws_dropped_total",
				Help:      "Total number of execution reports dropped for slow WS consumers.",
			},
		),
		riskDecision: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: cfg.Namespace,
				Subsystem: cfg.Subsystem,
				Name:      "risk_decision_total",
				Help:      "Total number of pre-trade risk decisions by code and result.",
			},
			[]string{"code", "result"},
		),
		feedMessages: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: cfg.Namespace,
				Subsystem: "market",
				Name:      "feed_messages_total",
				Help:      "Total number of decoded market-data messages by stream.",
			},
			[]string{"stream"},
		),
		feedReconnects: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: cfg.Namespace,
				Subsystem: "market",
				Name:      "feed_reconnects_total",
				Help:      "Total number of market-data feed reconnect attempts by provider.",
			},
			[]string{"provider"},
		),
		feedLag: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: cfg.Namespace,
				Subsystem: "market",
				Name:      "feed_lag_seconds",
				Help:      "Observed delay between venue event time and local receive time.",
			},
			[]string{"stream"},
		),
		feedDropped: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: cfg.Namespace,
				Subsystem: "market",
				Name:      "feed_dropped_total",
				Help:      "Total number of market-data events dropped for slow subscribers by topic.",
			},
			[]string{"topic"},
		),
	}

	if err := registry.Register(m.orderArrivalTotal); err != nil {
		return nil, err
	}
	if err := registry.Register(m.matchLatency); err != nil {
		return nil, err
	}
	if err := registry.Register(m.queueBacklog); err != nil {
		return nil, err
	}
	if err := registry.Register(m.queueDropped); err != nil {
		return nil, err
	}
	if err := registry.Register(m.sinkFailures); err != nil {
		return nil, err
	}
	if err := registry.Register(m.wsDropped); err != nil {
		return nil, err
	}
	if err := registry.Register(m.riskDecision); err != nil {
		return nil, err
	}
	if err := registry.Register(m.feedMessages); err != nil {
		return nil, err
	}
	if err := registry.Register(m.feedReconnects); err != nil {
		return nil, err
	}
	if err := registry.Register(m.feedLag); err != nil {
		return nil, err
	}
	if err := registry.Register(m.feedDropped); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *PrometheusMetrics) IncOrderArrival(result string) {
	if m == nil {
		return
	}
	if result == "" {
		result = "unknown"
	}
	m.orderArrivalTotal.WithLabelValues(result).Inc()
}

func (m *PrometheusMetrics) ObserveMatchLatency(result string, d time.Duration) {
	if m == nil {
		return
	}
	if result == "" {
		result = "unknown"
	}
	m.matchLatency.WithLabelValues(result).Observe(d.Seconds())
}

func (m *PrometheusMetrics) SetQueueBacklog(queue string, n float64) {
	if m == nil {
		return
	}
	if queue == "" {
		queue = "unknown"
	}
	m.queueBacklog.WithLabelValues(queue).Set(n)
}

func (m *PrometheusMetrics) IncQueueDrop(queue string) {
	if m == nil {
		return
	}
	if queue == "" {
		queue = "unknown"
	}
	m.queueDropped.WithLabelValues(queue).Inc()
}

func (m *PrometheusMetrics) IncSinkFailure(sink string) {
	if m == nil {
		return
	}
	if sink == "" {
		sink = "unknown"
	}
	m.sinkFailures.WithLabelValues(sink).Inc()
}

func (m *PrometheusMetrics) IncWSDrop() {
	if m == nil {
		return
	}
	m.wsDropped.Inc()
}

func (m *PrometheusMetrics) IncRiskDecision(code, result string) {
	if m == nil {
		return
	}
	if code == "" {
		code = "unknown"
	}
	if result == "" {
		result = "unknown"
	}
	m.riskDecision.WithLabelValues(code, result).Inc()
}

// IncFeedMessage records one decoded market-data message.
func (m *PrometheusMetrics) IncFeedMessage(stream string) {
	if m == nil {
		return
	}
	if stream == "" {
		stream = "unknown"
	}
	m.feedMessages.WithLabelValues(stream).Inc()
}

// IncFeedReconnect records one market-data reconnect attempt.
func (m *PrometheusMetrics) IncFeedReconnect(provider string) {
	if m == nil {
		return
	}
	if provider == "" {
		provider = "unknown"
	}
	m.feedReconnects.WithLabelValues(provider).Inc()
}

// SetFeedLag records the current feed delay for a stream.
func (m *PrometheusMetrics) SetFeedLag(stream string, d time.Duration) {
	if m == nil {
		return
	}
	if stream == "" {
		stream = "unknown"
	}
	m.feedLag.WithLabelValues(stream).Set(d.Seconds())
}

// IncFeedDrop records a market-data event dropped for a slow subscriber.
func (m *PrometheusMetrics) IncFeedDrop(topic string) {
	if m == nil {
		return
	}
	if topic == "" {
		topic = "unknown"
	}
	m.feedDropped.WithLabelValues(topic).Inc()
}

func (m *PrometheusMetrics) Handler() http.Handler {
	if m == nil {
		return promhttp.Handler()
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
