package metrics

import "github.com/prometheus/client_golang/prometheus"

// WorkflowMetrics contains only bounded labels. IDs and user-authored names
// are deliberately excluded so one workflow cannot create an unbounded series.
type WorkflowMetrics struct {
	Transitions          *prometheus.CounterVec
	Reconciliations      *prometheus.CounterVec
	FanOutChildren       prometheus.Histogram
	RunDuration          *prometheus.HistogramVec
	StepDuration         *prometheus.HistogramVec
	AcceptanceWait       prometheus.Histogram
	Callbacks            *prometheus.CounterVec
	OldestQueuedCallback prometheus.Gauge
	OldestStalledRun     prometheus.Gauge
}

func NewWorkflowMetrics() *WorkflowMetrics {
	return &WorkflowMetrics{
		Transitions:          prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "multica", Subsystem: "workflow", Name: "transitions_total", Help: "Recorded workflow state transitions by bounded event type."}, []string{"event"}),
		Reconciliations:      prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "multica", Subsystem: "workflow", Name: "reconciliations_total", Help: "Workflow reconciliation attempts by bounded outcome."}, []string{"outcome"}),
		FanOutChildren:       prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "multica", Subsystem: "workflow", Name: "fan_out_children", Help: "Children materialized by a fan-out step.", Buckets: []float64{1, 2, 4, 8, 16, 32, 64}}),
		RunDuration:          prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "multica", Subsystem: "workflow", Name: "run_duration_seconds", Help: "Terminal workflow run duration.", Buckets: prometheus.ExponentialBuckets(1, 4, 10)}, []string{"status"}),
		StepDuration:         prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "multica", Subsystem: "workflow", Name: "step_duration_seconds", Help: "Terminal workflow step duration.", Buckets: prometheus.ExponentialBuckets(0.1, 4, 10)}, []string{"node_type", "status"}),
		AcceptanceWait:       prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "multica", Subsystem: "workflow", Name: "acceptance_wait_seconds", Help: "Time spent waiting for a workflow acceptance decision.", Buckets: prometheus.ExponentialBuckets(1, 4, 10)}),
		Callbacks:            prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "multica", Subsystem: "workflow", Name: "callbacks_total", Help: "Workflow callback attempts by bounded outcome."}, []string{"outcome"}),
		OldestQueuedCallback: prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "multica", Subsystem: "workflow", Name: "oldest_queued_callback_seconds", Help: "Age of the oldest queued or dispatching workflow callback."}),
		OldestStalledRun:     prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "multica", Subsystem: "workflow", Name: "oldest_stalled_active_run_seconds", Help: "Age since the oldest active workflow Run was updated."}),
	}
}

func (m *WorkflowMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Transitions, m.Reconciliations, m.FanOutChildren, m.RunDuration, m.StepDuration, m.AcceptanceWait, m.Callbacks, m.OldestQueuedCallback, m.OldestStalledRun}
}

func (m *WorkflowMetrics) RecordEvent(event string) {
	if m != nil {
		m.Transitions.WithLabelValues(event).Inc()
	}
}
func (m *WorkflowMetrics) RecordReconciliation(outcome string) {
	if m != nil {
		m.Reconciliations.WithLabelValues(outcome).Inc()
	}
}
func (m *WorkflowMetrics) ObserveFanOut(children int) {
	if m != nil {
		m.FanOutChildren.Observe(float64(children))
	}
}
func (m *WorkflowMetrics) ObserveRun(status string, seconds float64) {
	if m != nil {
		m.RunDuration.WithLabelValues(status).Observe(seconds)
	}
}
func (m *WorkflowMetrics) ObserveStep(nodeType, status string, seconds float64) {
	if m != nil {
		m.StepDuration.WithLabelValues(nodeType, status).Observe(seconds)
	}
}
func (m *WorkflowMetrics) ObserveAcceptanceWait(seconds float64) {
	if m != nil {
		m.AcceptanceWait.Observe(seconds)
	}
}

func (m *WorkflowMetrics) RecordCallback(outcome string) {
	if m != nil {
		m.Callbacks.WithLabelValues(outcome).Inc()
	}
}

func (m *WorkflowMetrics) SetOldestQueuedCallback(seconds float64) {
	if m != nil {
		m.OldestQueuedCallback.Set(seconds)
	}
}

func (m *WorkflowMetrics) SetOldestStalledRun(seconds float64) {
	if m != nil {
		m.OldestStalledRun.Set(seconds)
	}
}
