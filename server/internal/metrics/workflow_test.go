package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestWorkflowMetricsUseOnlyBoundedLabels(t *testing.T) {
	m := NewWorkflowMetrics()
	for _, collector := range m.Collectors() {
		ch := make(chan *prometheus.Desc, 8)
		go func() { collector.Describe(ch); close(ch) }()
		for desc := range ch {
			text := desc.String()
			for _, forbidden := range []string{"workspace_id", "template_id", "run_id", "step_id", "issue_id", "agent_id"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("metric descriptor contains high-cardinality label %q: %s", forbidden, text)
				}
			}
		}
	}
}
