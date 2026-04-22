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

func (m *PrometheusMetrics) Handler() http.Handler {
	if m == nil {
		return promhttp.Handler()
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
