package workflow

import "fmt"

// Typed error codes (plan section 9). API handlers map these to stable
// machine-readable codes so a client can distinguish "your graph is wrong" from
// "this transition is illegal right now" from "you hit a budget ceiling"
// without string-matching messages.
const (
	ErrCodeInvalidDefinition   = "workflow_invalid_definition"
	ErrCodeInvalidTransition   = "workflow_invalid_transition"
	ErrCodeInvalidSubmission   = "workflow_invalid_submission"
	ErrCodeIdempotencyConflict = "workflow_idempotency_conflict"
	ErrCodeBudgetExceeded      = "workflow_budget_exceeded"
	ErrCodeRoutingFailed       = "workflow_routing_failed"
	ErrCodeAcceptanceConflict  = "workflow_acceptance_conflict"
	ErrCodeReworkLimit         = "workflow_rework_limit"
	ErrCodeInvariantViolation  = "workflow_invariant_violation"
	ErrCodeNotFound            = "workflow_not_found"
)

// Blocked/failure reason codes written to workflow_run.blocked_reason and
// workflow_step_instance.failure_reason. These are bounded, normalized labels:
// plan section 12 forbids unbounded Prometheus label values, and every terminal
// blocked/failed row must carry one of these so no Run is unexplained.
const (
	ReasonSubmissionContractInvalid = "submission_contract_invalid"
	ReasonInvariantViolation        = "workflow_invariant_violation"
	ReasonRoutingNoCandidate        = "routing_no_candidate"
	ReasonAttemptLimitExceeded      = "attempt_limit_exceeded"
	ReasonReworkLimitExceeded       = "rework_limit_exceeded"
	ReasonStepLimitExceeded         = "step_limit_exceeded"
	ReasonDurationLimitExceeded     = "duration_limit_exceeded"
	ReasonCostLimitExceeded         = "cost_limit_exceeded"
	ReasonAgentTaskFailed           = "agent_task_failed"
	ReasonAgentVerdictFail          = "agent_verdict_fail"
	ReasonAgentVerdictBlocked       = "agent_verdict_blocked"
	ReasonAcceptanceRejected        = "acceptance_rejected"
	ReasonActivationTimeout         = "activation_timeout"
	ReasonRuntimeOffline            = "runtime_offline"
	ReasonFanOutEmpty               = "fan_out_empty"
	ReasonFanOutLimitExceeded       = "fan_out_limit_exceeded"
	ReasonJoinChildFailed           = "join_child_failed"
	ReasonCancelled                 = "cancelled"
)

// DefinitionError reports a rejected graph. Field locates the offending node or
// edge so the template editor can point at it instead of showing a generic
// "invalid definition".
type DefinitionError struct {
	Code    string
	Message string
	Field   string
}

func (e *DefinitionError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Field)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// ValidationErrors aggregates every problem found in one pass. Publishing
// reports all of them at once: fixing a graph one error per round-trip is a
// poor authoring experience, and the checks are independent.
type ValidationErrors struct {
	Errors []*DefinitionError
}

func (v *ValidationErrors) Error() string {
	if len(v.Errors) == 0 {
		return "no validation errors"
	}
	if len(v.Errors) == 1 {
		return v.Errors[0].Error()
	}
	return fmt.Sprintf("%s (and %d more problems)", v.Errors[0].Error(), len(v.Errors)-1)
}

func (v *ValidationErrors) add(field, message string) {
	v.Errors = append(v.Errors, &DefinitionError{
		Code:    ErrCodeInvalidDefinition,
		Message: message,
		Field:   field,
	})
}

// HasErrors reports whether validation found anything.
func (v *ValidationErrors) HasErrors() bool { return len(v.Errors) > 0 }

// Messages returns the human-readable problems, for storing in
// workflow_submission.validation_errors or returning over the API.
func (v *ValidationErrors) Messages() []string {
	out := make([]string, 0, len(v.Errors))
	for _, e := range v.Errors {
		out = append(out, e.Error())
	}
	return out
}

// EngineError reports a rejected command. Unlike DefinitionError these are
// runtime conditions: an illegal transition, a replayed command, a blown budget.
type EngineError struct {
	Code    string
	Message string
	// Retryable distinguishes "this will never work" from "this lost a race and
	// the caller may try again", which the reconciler uses to decide whether to
	// replay a command or escalate to blocked.
	Retryable bool
}

func (e *EngineError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func newEngineError(code, message string) *EngineError {
	return &EngineError{Code: code, Message: message}
}

// IsInvalidTransition reports whether err is a rejected state transition, which
// callers treat as a 409 rather than a 500.
func IsInvalidTransition(err error) bool {
	e, ok := err.(*EngineError)
	return ok && e.Code == ErrCodeInvalidTransition
}

// IsIdempotencyConflict reports whether err is a replayed command. Callers
// return the existing resource instead of surfacing an error.
func IsIdempotencyConflict(err error) bool {
	e, ok := err.(*EngineError)
	return ok && e.Code == ErrCodeIdempotencyConflict
}
