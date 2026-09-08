package metrics

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// IssuePoolMetrics deliberately exposes only finite state/reason vocabularies.
// Workspace, autopilot, policy, issue, template, cycle, item and run identifiers
// must remain in logs/traces, never Prometheus labels.
type IssuePoolMetrics struct {
	Selection       *prometheus.CounterVec
	Exclusions      *prometheus.CounterVec
	ItemTransitions *prometheus.CounterVec
	CycleOutcomes   *prometheus.CounterVec
	Reconciliations *prometheus.CounterVec
	Notifications   *prometheus.CounterVec
	Duration        *prometheus.HistogramVec
	CurrentItems    *prometheus.GaugeVec
}

func NewIssuePoolMetrics() *IssuePoolMetrics {
	durationBuckets := prometheus.ExponentialBuckets(1, 4, 10)
	return &IssuePoolMetrics{
		Selection: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "issue_pool", Name: "selection_total",
			Help: "Issue-pool selection volume by bounded outcome.",
		}, []string{"outcome"}),
		Exclusions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "issue_pool", Name: "exclusions_total",
			Help: "Issue-pool preview exclusions by bounded rule.",
		}, []string{"reason"}),
		ItemTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "issue_pool", Name: "item_transitions_total",
			Help: "Issue-pool item transitions by bounded destination state.",
		}, []string{"state"}),
		CycleOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "issue_pool", Name: "cycle_outcomes_total",
			Help: "Issue-pool cycle terminal transitions by bounded outcome.",
		}, []string{"outcome"}),
		Reconciliations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "issue_pool", Name: "reconciliations_total",
			Help: "Issue-pool reconciliation passes by bounded result.",
		}, []string{"result"}),
		Notifications: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "issue_pool", Name: "notifications_total",
			Help: "Durable issue-pool notification delivery attempts by bounded result.",
		}, []string{"result"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "multica", Subsystem: "issue_pool", Name: "duration_seconds",
			Help: "Issue-pool latency by bounded stage.", Buckets: durationBuckets,
		}, []string{"stage"}),
		CurrentItems: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "multica", Subsystem: "issue_pool", Name: "current_items",
			Help: "Current issue-pool item count by bounded actionable state.",
		}, []string{"state"}),
	}
}

func (m *IssuePoolMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.Selection, m.Exclusions, m.ItemTransitions, m.CycleOutcomes,
		m.Reconciliations, m.Notifications, m.Duration, m.CurrentItems,
	}
}

var issuePoolExclusionReasons = map[string]struct{}{
	"status_not_eligible": {}, "priority_not_eligible": {}, "recently_active": {},
	"human_assignee": {}, "missing_description": {}, "missing_acceptance_criteria": {},
	"required_label_missing": {}, "excluded_label": {}, "property_mismatch": {},
	"active_task": {}, "active_workflow": {}, "blocking_dependency": {},
	"active_claim": {},
}

var issuePoolStates = map[string]struct{}{
	"claimed": {}, "approved": {}, "rejected": {}, "dispatching": {},
	"running": {}, "waiting_acceptance": {}, "blocked": {}, "completed": {},
	"failed": {}, "cancelled": {}, "deferred": {},
}

var issuePoolCycleOutcomes = map[string]struct{}{
	"completed": {}, "partial": {}, "failed": {}, "cancelled": {},
}

func bounded(value string, allowed map[string]struct{}) string {
	value = strings.TrimSpace(value)
	if _, ok := allowed[value]; ok {
		return value
	}
	return "other"
}

func (m *IssuePoolMetrics) AddSelection(outcome string, count int) {
	if m != nil && count > 0 {
		m.Selection.WithLabelValues(bounded(outcome, map[string]struct{}{
			"scanned": {}, "eligible": {}, "selected": {},
		})).Add(float64(count))
	}
}
func (m *IssuePoolMetrics) AddExclusion(reason string, count int) {
	if m != nil && count > 0 {
		m.Exclusions.WithLabelValues(bounded(reason, issuePoolExclusionReasons)).Add(float64(count))
	}
}
func (m *IssuePoolMetrics) AddItemTransition(state string, count int) {
	if m != nil && count > 0 {
		m.ItemTransitions.WithLabelValues(bounded(state, issuePoolStates)).Add(float64(count))
	}
}
func (m *IssuePoolMetrics) RecordCycleOutcome(outcome string) {
	if m != nil {
		m.CycleOutcomes.WithLabelValues(bounded(outcome, issuePoolCycleOutcomes)).Inc()
	}
}
func (m *IssuePoolMetrics) RecordReconciliation(result string) {
	if m != nil {
		m.Reconciliations.WithLabelValues(bounded(result, map[string]struct{}{
			"success": {}, "query_error": {}, "projection_error": {},
		})).Inc()
	}
}
func (m *IssuePoolMetrics) RecordNotification(result string) {
	if m != nil {
		m.Notifications.WithLabelValues(bounded(result, map[string]struct{}{
			"delivered": {}, "persist_error": {},
		})).Inc()
	}
}
func (m *IssuePoolMetrics) ObserveDuration(stage string, seconds float64) {
	if m != nil && seconds >= 0 {
		m.Duration.WithLabelValues(bounded(stage, map[string]struct{}{
			"backlog_age_at_claim": {}, "claim_to_dispatch": {}, "cycle": {},
		})).Observe(seconds)
	}
}
func (m *IssuePoolMetrics) SetCurrentItems(state string, count float64) {
	if m != nil {
		m.CurrentItems.WithLabelValues(bounded(state, map[string]struct{}{
			"active": {}, "pending_review": {}, "blocked": {},
		})).Set(count)
	}
}
