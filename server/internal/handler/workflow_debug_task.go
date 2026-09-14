package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"strings"
	"time"
)

func debugNullable(s string) pgtype.Text {
	return pgtype.Text{String: sanitizeNullBytes(s), Valid: s != ""}
}

// handleWorkflowDebugTask intercepts debug mutations before decoding a payload.
// Ordinary daemon requests keep their original handler and wire contract.
func (h *Handler) handleWorkflowDebugTask(w http.ResponseWriter, r *http.Request, action string) bool {
	taskValue := chi.URLParam(r, "taskId")
	var taskID pgtype.UUID
	if taskID.Scan(taskValue) != nil {
		return false
	}
	_, err := h.Queries.GetWorkflowDebugTaskRun(r.Context(), taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		h.writeDebugError(w, r, err)
		return true
	}
	if _, _, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskValue); !ok {
		return true
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return true
	}
	claimID, ok := parseUUIDOrBadRequest(w, r.Header.Get(workflow.DebugExecutionHeader), "execution id")
	if !ok {
		return true
	}
	// The existing runtime loader proves daemon/token ownership, including the
	// original runtime after a task was cancelled or rebound by maintenance.
	claim, err := h.Queries.GetWorkflowDebugExecution(r.Context(), claimID)
	if err != nil {
		writeError(w, 403, "execution identity does not match")
		return true
	}
	if _, ok = h.requireDaemonRuntimeAccess(w, r, uuidToString(claim.RuntimeID)); !ok {
		return true
	}
	authenticate := func(_ context.Context, x db.WorkflowDebugTaskExecution) error {
		if x.ID != claim.ID || x.RuntimeID != claim.RuntimeID || x.DaemonIncarnationID != claim.DaemonIncarnationID {
			return &workflow.EngineError{Code: "debug_forbidden", Message: "execution owner changed"}
		}
		return nil
	}
	if action == "execution-receipts" {
		var receipt workflow.DebugExecutionReceipt
		if !decodeDebugRequest(w, r, &receipt) {
			return true
		}
		if err = engine.AcceptDebugExecutionReceipt(r.Context(), taskID, claimID, authenticate, receipt); err != nil {
			h.writeDebugError(w, r, err)
		} else {
			writeJSON(w, 200, map[string]string{"status": "accepted"})
		}
		return true
	}
	disposition := "accepted"
	var report *workflow.RecordTaskTerminalInput
	err = engine.WithDebugTaskExecution(r.Context(), taskID, claimID, authenticate, func(ctx context.Context, q *db.Queries, id workflow.DebugTaskIdentity) error {
		terminal := action == "complete" || action == "fail"
		if ignored := workflow.DebugPayloadDisposition(id, terminal); ignored != "" {
			disposition = ignored
			return nil
		}
		// Decode only inside the run/step/task/claim lock, after identity and expiry.
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		decode := func(v any) error {
			if err := json.NewDecoder(r.Body).Decode(v); err != nil {
				return &workflow.EngineError{Code: "debug_bad_request", Message: "invalid task report"}
			}
			return nil
		}
		switch action {
		case "start":
			if workflow.IsTerminalRunStatus(workflow.RunStatus(id.Run.Status)) {
				disposition = "ignored_terminal"
				return nil
			}
			if _, err := q.StartWorkflowDebugTask(ctx, taskID); err != nil {
				return err
			}
			_, err := q.MarkWorkflowStepRunning(ctx, db.MarkWorkflowStepRunningParams{ID: id.Step.ID, WorkspaceID: id.Run.WorkspaceID})
			return err
		case "complete", "fail":
			var output, errorMessage, failure, session, workdir, durable, branch, retired string
			var raw []byte
			status := "completed"
			if action == "complete" {
				var req TaskCompleteRequest
				if err := decode(&req); err != nil {
					return err
				}
				sanitizeTaskCompleteRequest(&req)
				output = req.Output
				session = req.SessionID
				workdir = req.WorkDir
				durable = req.DurableWorkDir
				branch = req.BranchName
				retired = req.RetiredSessionID
				raw, _ = json.Marshal(req)
			} else {
				var req TaskFailRequest
				if err := decode(&req); err != nil {
					return err
				}
				sanitizeTaskFailRequest(&req)
				errorMessage = req.Error
				failure = req.FailureReason
				session = req.SessionID
				workdir = req.WorkDir
				durable = req.DurableWorkDir
				branch = req.BranchName
				retired = req.RetiredSessionID
				status = "failed"
			}
			changed, err := q.CompleteWorkflowDebugTask(ctx, db.CompleteWorkflowDebugTaskParams{ID: taskID, Status: status, Result: raw, Error: debugNullable(errorMessage), FailureReason: debugNullable(failure), SessionID: debugNullable(session), WorkDir: debugNullable(workdir), DurableWorkDir: debugNullable(durable), BranchName: debugNullable(branch), RetiredSessionID: debugNullable(retired)})
			if err != nil {
				return err
			}
			if changed > 0 {
				report = &workflow.RecordTaskTerminalInput{TaskID: taskID, TaskStatus: status, Result: output, FailureReason: failure, ErrorDetail: errorMessage}
			}
			return nil
		case "messages":
			var req TaskMessageBatchRequest
			if err := decode(&req); err != nil {
				return err
			}
			for _, m := range req.Messages {
				if m.Seq < 1 {
					return &workflow.EngineError{Code: "debug_bad_request", Message: "message sequence must be positive"}
				}
				var input []byte
				if m.Input != nil {
					input, _ = json.Marshal(m.Input)
				}
				timestamp := time.Now()
				if m.CreatedAt != nil {
					timestamp = *m.CreatedAt
				}
				truncated := pgtype.Bool{}
				if m.OutputTruncated != nil {
					truncated = pgtype.Bool{Bool: *m.OutputTruncated, Valid: true}
				}
				if err := q.InsertWorkflowDebugTaskMessage(ctx, db.InsertWorkflowDebugTaskMessageParams{TaskID: taskID, Seq: int32(m.Seq), Type: m.Type, Tool: debugNullable(m.Tool), Content: debugNullable(m.Content), Input: input, Output: debugNullable(m.Output), CreatedAt: pgtype.Timestamptz{Time: timestamp, Valid: true}, OutputTruncated: truncated}); err != nil {
					return err
				}
			}
			return nil
		case "session":
			var req struct {
				SessionID      string `json:"session_id"`
				WorkDir        string `json:"work_dir"`
				DurableWorkDir string `json:"durable_work_dir"`
				BranchName     string `json:"branch_name"`
			}
			if err := decode(&req); err != nil {
				return err
			}
			return q.SetWorkflowDebugTaskSession(ctx, db.SetWorkflowDebugTaskSessionParams{ID: taskID, SessionID: debugNullable(req.SessionID), WorkDir: debugNullable(req.WorkDir), DurableWorkDir: debugNullable(req.DurableWorkDir), BranchName: debugNullable(req.BranchName)})
		case "cancel-ack":
			var req struct {
				BranchName     string `json:"branch_name"`
				DurableWorkDir string `json:"durable_work_dir"`
				ErrorMessage   string `json:"error_message"`
			}
			if err := decode(&req); err != nil {
				return err
			}
			return q.SetWorkflowDebugTaskCancelAck(ctx, db.SetWorkflowDebugTaskCancelAckParams{ID: taskID, BranchName: debugNullable(req.BranchName), DurableWorkDir: debugNullable(req.DurableWorkDir), Error: debugNullable(req.ErrorMessage)})
		case "wait-local-directory":
			var req struct {
				Reason string `json:"reason"`
			}
			if err := decode(&req); err != nil {
				return err
			}
			_, err := q.SetWorkflowDebugTaskWaiting(ctx, db.SetWorkflowDebugTaskWaitingParams{ID: taskID, WaitReason: debugNullable(req.Reason)})
			return err
		case "usage":
			var req struct {
				Usage []TaskUsagePayload `json:"usage"`
			}
			if err := decode(&req); err != nil {
				return err
			}
			for _, u := range req.Usage {
				if len(u.Model) > 128 || len(u.Provider) > 64 || strings.ContainsAny(u.Model+u.Provider, "\n\r\x00") {
					return &workflow.EngineError{Code: "debug_bad_request", Message: "invalid usage identity"}
				}
				if err := q.UpsertTaskUsage(ctx, db.UpsertTaskUsageParams{TaskID: taskID, Provider: normalizeProvider(u.Provider), Model: u.Model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens, CostUsdTicks: authoritativeCostTicks(u.CostUSDTicks)}); err != nil {
					return err
				}
			}
			return nil
		case "progress":
			// Progress is ephemeral; the shared trace is refreshed from durable messages.
			return nil
		default:
			return &workflow.EngineError{Code: "debug_bad_request", Message: "unsupported debug task mutation"}
		}
	})
	if err != nil {
		h.writeDebugError(w, r, err)
		return true
	}
	if report != nil {
		if err = engine.RecordTaskTerminal(r.Context(), *report); err != nil {
			h.writeDebugError(w, r, err)
			return true
		}
	}
	writeJSON(w, 200, map[string]string{"status": disposition})
	return true
}
func (h *Handler) AcceptWorkflowDebugReceipt(w http.ResponseWriter, r *http.Request) {
	if !h.handleWorkflowDebugTask(w, r, "execution-receipts") {
		writeError(w, 404, "draft trial task not found")
	}
}
