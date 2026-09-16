package service

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func TestCapacityRetryBudgetAndCooldown(t *testing.T) {
	reason := string(taskfailure.ReasonAgentProviderCapacityOrRateLimit)
	for _, tc := range []struct {
		attempt, max, ceiling int32
		retry                 bool
	}{
		{1, 1, 1, false}, {1, 2, 2, true}, {2, 2, 2, false},
		{2, 3, 3, true}, {3, 3, 3, false}, {3, 10, 3, false},
	} {
		task := db.AgentTaskQueue{Attempt: tc.attempt, MaxAttempts: tc.max, IssueID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}}
		if got := retryEligible(reason, task); got != tc.retry {
			t.Errorf("attempt %d/%d retry = %v", tc.attempt, tc.max, got)
		}
		if got := retryAttemptCeiling(reason, tc.max); got != tc.ceiling {
			t.Errorf("max %d ceiling = %d", tc.max, got)
		}
	}
	for _, tc := range []struct {
		attempt int32
		delay   time.Duration
	}{{1, 30 * time.Second}, {2, 60 * time.Second}, {100, 60 * time.Second}} {
		if got := retryDelayForAttempt(reason, tc.attempt); got != tc.delay {
			t.Errorf("attempt %d delay = %s", tc.attempt, got)
		}
	}
	for _, reason := range []string{string(taskfailure.ReasonAgentModelNotFoundOrUnavailable), string(taskfailure.ReasonAgentProviderQuotaLimit), string(taskfailure.ReasonAgentProcessFailure)} {
		if retryableReasons[reason] {
			t.Errorf("deterministic failure %s must not retry", reason)
		}
	}
}
