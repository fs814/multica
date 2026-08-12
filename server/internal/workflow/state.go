package workflow

import "fmt"

// Run and Step state machines, transcribed from plan section 6.
//
// These are explicit transition tables rather than scattered `if status ==`
// checks, for two reasons. First, the tables are the specification: the plan
// enumerates the legal edges, and a table can be read against it line by line.
// Second, every engine command validates through the same function, so there is
// exactly one place where an illegal transition can be admitted — which is what
// lets the invariant "a Run completes only through End" actually hold.
//
// The SQL guards in workflow.sql are the second line of defence: each state
// mutation carries a `WHERE status IN (...)` clause, so even a bug here cannot
// corrupt a row without the update silently affecting zero rows.

// RunStatus mirrors the CHECK constraint on workflow_run.status.
type RunStatus string

const (
	RunPending           RunStatus = "pending"
	RunRunning           RunStatus = "running"
	RunWaitingAcceptance RunStatus = "waiting_acceptance"
	RunBlocked           RunStatus = "blocked"
	RunCompleted         RunStatus = "completed"
	RunFailed            RunStatus = "failed"
	RunCancelled         RunStatus = "cancelled"
)

// StepStatus mirrors the CHECK constraint on workflow_step_instance.status.
type StepStatus string

const (
	StepPending           StepStatus = "pending"
	StepReady             StepStatus = "ready"
	StepQueued            StepStatus = "queued"
	StepRunning           StepStatus = "running"
	StepSubmitted         StepStatus = "submitted"
	StepPassed            StepStatus = "passed"
	StepFailed            StepStatus = "failed"
	StepBlocked           StepStatus = "blocked"
	StepWaitingAcceptance StepStatus = "waiting_acceptance"
	StepSkipped           StepStatus = "skipped"
	StepCancelled         StepStatus = "cancelled"
)

// runTransitions is the adjacency list from plan section 6:
//
//	pending            -> running
//	running            -> waiting_acceptance / blocked / completed / failed / cancelled
//	waiting_acceptance -> running / completed / cancelled
//	blocked            -> running / failed / cancelled
//
// Terminal states have no outgoing edges, which is what makes a completed Run
// permanently completed.
var runTransitions = map[RunStatus]map[RunStatus]bool{
	RunPending: {
		RunRunning: true,
		// A Run can be cancelled or fail before it ever activates a Step (e.g.
		// routing finds no candidate at start), so these are reachable from
		// pending even though the plan's happy path goes through running.
		RunCancelled: true,
		RunFailed:    true,
		RunBlocked:   true,
	},
	RunRunning: {
		RunWaitingAcceptance: true,
		RunBlocked:           true,
		RunCompleted:         true,
		RunFailed:            true,
		RunCancelled:         true,
	},
	RunWaitingAcceptance: {
		RunRunning:   true,
		RunCompleted: true,
		RunCancelled: true,
		// A reviewer-rejected Run whose rework budget is spent fails outright
		// rather than looping; the alternative is a Run stuck awaiting a
		// decision that can no longer change anything.
		RunFailed: true,
	},
	RunBlocked: {
		RunRunning:   true,
		RunFailed:    true,
		RunCancelled: true,
	},
	RunCompleted: {},
	RunFailed:    {},
	RunCancelled: {},
}

// stepTransitions is the adjacency list from plan section 6:
//
//	pending            -> ready / skipped / cancelled
//	ready              -> queued / waiting_acceptance / passed
//	queued             -> running / failed / blocked / cancelled
//	running            -> submitted / failed / blocked / cancelled
//	submitted          -> passed / failed / blocked
//	waiting_acceptance -> passed / failed / cancelled
var stepTransitions = map[StepStatus]map[StepStatus]bool{
	StepPending: {
		StepReady:     true,
		StepSkipped:   true,
		StepCancelled: true,
	},
	StepReady: {
		StepQueued:            true,
		StepWaitingAcceptance: true,
		// Condition/FanOut/End nodes pass without ever being queued to an Agent.
		StepPassed:    true,
		StepCancelled: true,
		// Routing failure is detected at activation, before a Task exists.
		StepFailed:  true,
		StepBlocked: true,
		StepSkipped: true,
	},
	StepQueued: {
		StepRunning:   true,
		StepFailed:    true,
		StepBlocked:   true,
		StepCancelled: true,
		// A Task can complete so fast that the terminal event arrives before any
		// running notification; allowing queued -> submitted avoids rejecting a
		// legitimately finished Task as an illegal transition.
		StepSubmitted: true,
	},
	StepRunning: {
		StepSubmitted: true,
		StepFailed:    true,
		StepBlocked:   true,
		StepCancelled: true,
	},
	StepSubmitted: {
		StepPassed:  true,
		StepFailed:  true,
		StepBlocked: true,
		// A submitted Step on an Acceptance-gated node waits for a human.
		StepWaitingAcceptance: true,
		StepCancelled:         true,
	},
	StepWaitingAcceptance: {
		StepPassed:    true,
		StepFailed:    true,
		StepCancelled: true,
	},
	StepPassed:    {},
	StepFailed:    {},
	StepBlocked:   {},
	StepSkipped:   {},
	StepCancelled: {},
}

// terminalRunStatuses and terminalStepStatuses are derived from the tables above
// (no outgoing edges) but written explicitly so a reader does not have to infer
// them, and so IsTerminal stays O(1).
var terminalRunStatuses = map[RunStatus]bool{
	RunCompleted: true,
	RunFailed:    true,
	RunCancelled: true,
}

var terminalStepStatuses = map[StepStatus]bool{
	StepPassed:    true,
	StepFailed:    true,
	StepBlocked:   true,
	StepSkipped:   true,
	StepCancelled: true,
}

// IsTerminalRunStatus reports whether a Run can no longer change.
func IsTerminalRunStatus(s RunStatus) bool { return terminalRunStatuses[s] }

// IsTerminalStepStatus reports whether a Step attempt can no longer change.
// Note that blocked is terminal for the attempt: recovery creates a NEW attempt
// rather than reviving this one, which is what preserves the failed attempt's
// Submission history for the rework context.
func IsTerminalStepStatus(s StepStatus) bool { return terminalStepStatuses[s] }

// ValidateRunTransition returns nil when from -> to is legal.
//
// A self-transition is allowed only where the corresponding SQL is idempotent
// (running -> running when resuming). Everything else returns
// ErrCodeInvalidTransition, which handlers surface as a 409.
func ValidateRunTransition(from, to RunStatus) error {
	if from == to {
		// Replayed commands are normal in this system (durable webhooks, daemon
		// retries, reconciler replays). Treating a no-op restatement of the
		// current state as an error would make the reconciler's job impossible.
		if from == RunRunning || from == RunBlocked || from == RunWaitingAcceptance {
			return nil
		}
		return newEngineError(ErrCodeInvalidTransition,
			fmt.Sprintf("run is already %s", from))
	}
	allowed, known := runTransitions[from]
	if !known {
		return newEngineError(ErrCodeInvalidTransition,
			fmt.Sprintf("unknown run status %q", from))
	}
	if !allowed[to] {
		return newEngineError(ErrCodeInvalidTransition,
			fmt.Sprintf("illegal run transition %s -> %s", from, to))
	}
	return nil
}

// ValidateStepTransition returns nil when from -> to is legal.
func ValidateStepTransition(from, to StepStatus) error {
	if from == to {
		if from == StepRunning {
			// Repeated running notifications from a daemon are expected.
			return nil
		}
		return newEngineError(ErrCodeInvalidTransition,
			fmt.Sprintf("step is already %s", from))
	}
	allowed, known := stepTransitions[from]
	if !known {
		return newEngineError(ErrCodeInvalidTransition,
			fmt.Sprintf("unknown step status %q", from))
	}
	if !allowed[to] {
		return newEngineError(ErrCodeInvalidTransition,
			fmt.Sprintf("illegal step transition %s -> %s", from, to))
	}
	return nil
}

// IssueStatusForRun projects a Run's state onto its Issue (plan section 6):
//
//	running -> in_progress, waiting_acceptance -> in_review,
//	blocked -> blocked, completed (via End) -> done,
//	failed -> todo plus a failure activity.
//
// The Issue is a projection, never the source of truth — Workflow owns
// execution. Returning ok=false means "leave the Issue alone", which is the
// correct behavior for pending and cancelled Runs.
func IssueStatusForRun(s RunStatus) (string, bool) {
	switch s {
	case RunRunning:
		return "in_progress", true
	case RunWaitingAcceptance:
		return "in_review", true
	case RunBlocked:
		return "blocked", true
	case RunCompleted:
		return "done", true
	case RunFailed:
		// Back to todo: the work still needs doing, and the failure detail lands
		// as an activity so the reason is not lost.
		return "todo", true
	default:
		return "", false
	}
}

// VerdictToStepStatus maps a submission verdict onto the Step outcome for a node
// with no acceptance gate. A pass on an acceptance-gated node goes to
// waiting_acceptance instead, which the engine decides — not this function.
func VerdictToStepStatus(v Verdict) (StepStatus, string) {
	switch v {
	case VerdictPass:
		return StepPassed, ""
	case VerdictFail:
		return StepFailed, ReasonAgentVerdictFail
	case VerdictBlocked:
		return StepBlocked, ReasonAgentVerdictBlocked
	default:
		// Defensive: ParseSubmission rejects unknown verdicts, so reaching here
		// means a caller bypassed it. Block rather than guess.
		return StepBlocked, ReasonSubmissionContractInvalid
	}
}
