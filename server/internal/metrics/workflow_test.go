package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

func TestWorkflowMetricsRecordCallbackOutcomesAndBacklogAge(t *testing.T) {
	m := NewWorkflowMetrics()
	m.RecordCallback("delivered")
	m.RecordCallback("retry")
	m.RecordCallback("failed")
	m.SetOldestQueuedCallback(17)
	m.SetOldestStalledRun(29)

	if got := testutil.ToFloat64(m.Callbacks.WithLabelValues("delivered")); got != 1 {
		t.Fatalf("delivered callbacks = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.Callbacks.WithLabelValues("retry")); got != 1 {
		t.Fatalf("retry callbacks = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.Callbacks.WithLabelValues("failed")); got != 1 {
		t.Fatalf("failed callbacks = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.OldestQueuedCallback); got != 17 {
		t.Fatalf("oldest queued callback = %v, want 17", got)
	}
	if got := testutil.ToFloat64(m.OldestStalledRun); got != 29 {
		t.Fatalf("oldest stalled Run = %v, want 29", got)
	}
}
