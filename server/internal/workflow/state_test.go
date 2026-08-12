package workflow

import "testing"

// TestRunTransitionsMatchPlan transcribes the Run adjacency list from plan
// section 6 and asserts the table agrees with it, including that everything NOT
// listed is rejected. The exhaustive negative sweep is the point: a typo that
// accidentally admits running -> completed without End would otherwise pass a
// happy-path test.
func TestRunTransitionsMatchPlan(t *testing.T) {
	legal := map[RunStatus][]RunStatus{
		RunPending:           {RunRunning, RunBlocked, RunFailed, RunCancelled},
		RunRunning:           {RunWaitingAcceptance, RunBlocked, RunCompleted, RunFailed, RunCancelled},
		RunWaitingAcceptance: {RunRunning, RunCompleted, RunFailed, RunCancelled},
		RunBlocked:           {RunRunning, RunFailed, RunCancelled},
		RunCompleted:         {},
		RunFailed:            {},
		RunCancelled:         {},
	}
	all := []RunStatus{
		RunPending, RunRunning, RunWaitingAcceptance, RunBlocked,
		RunCompleted, RunFailed, RunCancelled,
	}

	for from, allowed := range legal {
		allowedSet := make(map[RunStatus]bool, len(allowed))
		for _, to := range allowed {
			allowedSet[to] = true
			if err := ValidateRunTransition(from, to); err != nil {
				t.Errorf("%s -> %s must be legal, got: %v", from, to, err)
			}
		}
		for _, to := range all {
			if from == to || allowedSet[to] {
				continue
			}
			if err := ValidateRunTransition(from, to); err == nil {
				t.Errorf("%s -> %s must be rejected, but it was allowed", from, to)
			}
		}
	}
}

func TestStepTransitionsMatchPlan(t *testing.T) {
	legal := map[StepStatus][]StepStatus{
		StepPending: {StepReady, StepSkipped, StepCancelled},
		// ready fans out wider than the plan's minimum because non-agent nodes
		// (condition/fan_out/end) pass without queueing, and routing failure is
		// detected at activation before any Task exists.
		StepReady:             {StepQueued, StepWaitingAcceptance, StepPassed, StepFailed, StepBlocked, StepSkipped, StepCancelled},
		StepQueued:            {StepRunning, StepSubmitted, StepFailed, StepBlocked, StepCancelled},
		StepRunning:           {StepSubmitted, StepFailed, StepBlocked, StepCancelled},
		StepSubmitted:         {StepPassed, StepFailed, StepBlocked, StepWaitingAcceptance, StepCancelled},
		StepWaitingAcceptance: {StepPassed, StepFailed, StepCancelled},
		StepPassed:            {},
		StepFailed:            {},
		StepBlocked:           {},
		StepSkipped:           {},
		StepCancelled:         {},
	}
	all := []StepStatus{
		StepPending, StepReady, StepQueued, StepRunning, StepSubmitted,
		StepPassed, StepFailed, StepBlocked, StepWaitingAcceptance,
		StepSkipped, StepCancelled,
	}

	for from, allowed := range legal {
		allowedSet := make(map[StepStatus]bool, len(allowed))
		for _, to := range allowed {
			allowedSet[to] = true
			if err := ValidateStepTransition(from, to); err != nil {
				t.Errorf("%s -> %s must be legal, got: %v", from, to, err)
			}
		}
		for _, to := range all {
			if from == to || allowedSet[to] {
				continue
			}
			if err := ValidateStepTransition(from, to); err == nil {
				t.Errorf("%s -> %s must be rejected, but it was allowed", from, to)
			}
		}
	}
}

// TestTerminalStatesAreFinal is the schema-level guarantee behind "published
// versions and completed Step attempts are immutable": once terminal, no
// transition out exists at all.
func TestTerminalStatesAreFinal(t *testing.T) {
	for _, from := range []RunStatus{RunCompleted, RunFailed, RunCancelled} {
		if !IsTerminalRunStatus(from) {
			t.Errorf("%s should be terminal", from)
		}
		for _, to := range []RunStatus{RunPending, RunRunning, RunWaitingAcceptance, RunBlocked, RunCompleted, RunFailed, RunCancelled} {
			if err := ValidateRunTransition(from, to); err == nil {
				t.Errorf("terminal run %s must not transition to %s", from, to)
			}
		}
	}
	for _, from := range []StepStatus{StepPassed, StepFailed, StepBlocked, StepSkipped, StepCancelled} {
		if !IsTerminalStepStatus(from) {
			t.Errorf("%s should be terminal", from)
		}
		for _, to := range []StepStatus{StepPending, StepReady, StepQueued, StepRunning, StepSubmitted, StepPassed} {
			if err := ValidateStepTransition(from, to); err == nil {
				t.Errorf("terminal step %s must not transition to %s", from, to)
			}
		}
	}
}

// TestRunCannotCompleteFromNonRunning encodes the headline invariant: "a Run
// completes only through End". End execution requires a running Run, so
// completing from pending or blocked must be impossible.
func TestRunCannotCompleteFromNonRunning(t *testing.T) {
	for _, from := range []RunStatus{RunPending, RunBlocked} {
		if err := ValidateRunTransition(from, RunCompleted); err == nil {
			t.Errorf("a Run must not complete directly from %s; only End completes a Run", from)
		}
	}
	// waiting_acceptance -> completed IS legal: accepting the final gate is what
	// lets the End node run.
	if err := ValidateRunTransition(RunWaitingAcceptance, RunCompleted); err != nil {
		t.Errorf("accepting the final gate must be able to complete a Run: %v", err)
	}
}

// TestIdempotentSelfTransitions: replayed commands are normal here (durable
// webhooks, daemon retries, reconciler replays), so restating the current state
// must not be an error where the corresponding SQL is idempotent.
func TestIdempotentSelfTransitions(t *testing.T) {
	for _, s := range []RunStatus{RunRunning, RunBlocked, RunWaitingAcceptance} {
		if err := ValidateRunTransition(s, s); err != nil {
			t.Errorf("replaying %s -> %s must be a no-op, got: %v", s, s, err)
		}
	}
	if err := ValidateStepTransition(StepRunning, StepRunning); err != nil {
		t.Errorf("repeated running notifications must be tolerated: %v", err)
	}
	// But re-completing a terminal state is a real conflict, not a no-op.
	if err := ValidateRunTransition(RunCompleted, RunCompleted); err == nil {
		t.Error("re-completing a completed Run must be rejected")
	}
	if err := ValidateStepTransition(StepPassed, StepPassed); err == nil {
		t.Error("re-passing a passed Step must be rejected")
	}
}

func TestInvalidTransitionErrorIsTyped(t *testing.T) {
	err := ValidateRunTransition(RunCompleted, RunRunning)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsInvalidTransition(err) {
		t.Errorf("error should be classified as invalid transition, got %v", err)
	}
}

func TestUnknownStatusIsRejected(t *testing.T) {
	if err := ValidateRunTransition(RunStatus("bogus"), RunRunning); err == nil {
		t.Error("unknown run status must be rejected")
	}
	if err := ValidateStepTransition(StepStatus("bogus"), StepReady); err == nil {
		t.Error("unknown step status must be rejected")
	}
}

func TestIssueStatusProjection(t *testing.T) {
	cases := map[RunStatus]struct {
		want string
		ok   bool
	}{
		RunRunning:           {"in_progress", true},
		RunWaitingAcceptance: {"in_review", true},
		RunBlocked:           {"blocked", true},
		RunCompleted:         {"done", true},
		RunFailed:            {"todo", true},
		// pending and cancelled leave the Issue alone: the Issue is a projection,
		// and there is no meaningful status to project yet / anymore.
		RunPending:   {"", false},
		RunCancelled: {"", false},
	}
	for run, want := range cases {
		got, ok := IssueStatusForRun(run)
		if got != want.want || ok != want.ok {
			t.Errorf("IssueStatusForRun(%s) = (%q, %v), want (%q, %v)", run, got, ok, want.want, want.ok)
		}
	}
}

func TestVerdictToStepStatus(t *testing.T) {
	if s, _ := VerdictToStepStatus(VerdictPass); s != StepPassed {
		t.Errorf("pass -> %s, want %s", s, StepPassed)
	}
	s, reason := VerdictToStepStatus(VerdictFail)
	if s != StepFailed || reason != ReasonAgentVerdictFail {
		t.Errorf("fail -> (%s, %s), want (%s, %s)", s, reason, StepFailed, ReasonAgentVerdictFail)
	}
	s, reason = VerdictToStepStatus(VerdictBlocked)
	if s != StepBlocked || reason != ReasonAgentVerdictBlocked {
		t.Errorf("blocked -> (%s, %s), want (%s, %s)", s, reason, StepBlocked, ReasonAgentVerdictBlocked)
	}
	// An unknown verdict must block, never pass: guessing would be a false
	// success, which is the failure mode the whole contract exists to prevent.
	s, reason = VerdictToStepStatus(Verdict("probably?"))
	if s != StepBlocked || reason != ReasonSubmissionContractInvalid {
		t.Errorf("unknown verdict -> (%s, %s), want (%s, %s)", s, reason, StepBlocked, ReasonSubmissionContractInvalid)
	}
}
