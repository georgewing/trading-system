package observability

import "testing"

func TestIncRiskDecisionDefensiveLabels(t *testing.T) {
	m, err := NewPrometheusMetrics(DefaultPrometheusConfig())
	if err != nil {
		t.Fatalf("NewPrometheusMetrics() error = %v", err)
	}

	m.IncRiskDecision("", "")
	m.IncRiskDecision("MAX_QTY", "rejected")

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}

	const name = "trading_system_matching_risk_decision_total"
	got := map[string]float64{}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			got[labels["code"]+"/"+labels["result"]] = metric.GetCounter().GetValue()
		}
	}

	if len(got) != 2 {
		t.Fatalf("risk decision series = %v, want 2 series", got)
	}
	if v, ok := got["unknown/unknown"]; !ok || v != 1 {
		t.Fatalf("empty labels series = %v (present=%v), want 1", v, ok)
	}
	if v, ok := got["MAX_QTY/rejected"]; !ok || v != 1 {
		t.Fatalf("MAX_QTY/rejected series = %v (present=%v), want 1", v, ok)
	}
}
