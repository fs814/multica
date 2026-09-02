package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestIssuePoolMetricsUseOnlyBoundedLabelsAndValues(t *testing.T) {
	m := NewIssuePoolMetrics()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(m.Collectors()...)

	m.AddExclusion("workspace-e8f1-private", 1)
	m.AddItemTransition("issue-123", 1)
	m.RecordCycleOutcome("customer-policy-name")
	m.RecordReconciliation("database-hostname")
	m.RecordNotification("recipient-id")
	m.ObserveDuration("template-id", 2)
	m.SetCurrentItems("workspace-id", 3)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetValue() != "other" {
					t.Fatalf("unexpected unbounded label value %q in %s", label.GetValue(), family.GetName())
				}
				for _, forbidden := range []string{"workspace", "issue-123", "customer", "recipient", "template"} {
					if strings.Contains(label.GetValue(), forbidden) {
						t.Fatalf("high-cardinality value leaked into %s", family.GetName())
					}
				}
			}
		}
	}
	if got := testutil.ToFloat64(m.Exclusions.WithLabelValues("other")); got != 1 {
		t.Fatalf("other exclusion = %v, want 1", got)
	}
}

func TestIssuePoolMetricsDescriptorsExcludeIdentifiers(t *testing.T) {
	m := NewIssuePoolMetrics()
	for _, collector := range m.Collectors() {
		ch := make(chan *prometheus.Desc, 8)
		go func() { collector.Describe(ch); close(ch) }()
		for desc := range ch {
			text := desc.String()
			for _, forbidden := range []string{
				"workspace_id", "autopilot_id", "policy_id", "issue_id",
				"template_id", "cycle_id", "item_id", "run_id",
			} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("metric descriptor contains %q: %s", forbidden, text)
				}
			}
		}
	}
}
