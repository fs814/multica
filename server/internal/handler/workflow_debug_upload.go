package handler

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

const debugUploadSettleTimeout = 30 * time.Second

// Debug uploads use an intent before the object write. An interrupted upload
// remains pending, so no stop receipt or retention pass can race an in-flight
// object write. Exclusive artifacts always retain a durable task binding.
func (h *Handler) handleWorkflowDebugUpload(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		return false
	}
	var taskID pgtype.UUID
	if taskID.Scan(r.Header.Get("X-Task-ID")) != nil {
		return false
	}
	run, err := h.Queries.GetWorkflowDebugTaskRun(r.Context(), taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		h.writeDebugError(w, r, err)
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
	user, ok := requireUserID(w, r)
	if !ok {
		return true
	}
	if _, ok = h.requireWorkspaceMember(w, r, uuidToString(run.WorkspaceID), "attachment not found"); !ok {
		return true
	}
	authenticate := func(_ context.Context, x db.WorkflowDebugTaskExecution) error {
		if x.TaskID != taskID || x.WorkspaceID != run.WorkspaceID {
			return &workflow.EngineError{Code: "debug_forbidden", Message: "upload execution mismatch"}
		}
		return nil
	}
	var att db.Attachment
	var upload db.WorkflowDebugUpload
	var data []byte
	var key string
	ignored := ""
	err = engine.WithDebugTaskExecution(r.Context(), taskID, claimID, authenticate, func(ctx context.Context, q *db.Queries, id workflow.DebugTaskIdentity) error {
		ignored = workflow.DebugPayloadDisposition(id, false)
		if ignored != "" {
			return nil
		}
		if h.Storage == nil {
			return &workflow.EngineError{Code: "debug_unavailable", Message: "file upload not configured"}
		}
		if r.Header.Get("X-Agent-ID") != uuidToString(id.Task.AgentID) {
			return &workflow.EngineError{Code: "debug_forbidden", Message: "upload agent mismatch"}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
		if err := r.ParseMultipartForm(maxUploadSize); err != nil {
			return &workflow.EngineError{Code: "debug_bad_request", Message: "invalid file upload"}
		}
		defer r.MultipartForm.RemoveAll()
		for _, field := range []string{"issue_id", "comment_id", "chat_session_id"} {
			if r.FormValue(field) != "" {
				return &workflow.EngineError{Code: "debug_bad_request", Message: "trial artifacts cannot select a business destination"}
			}
		}
		if value := r.FormValue("task_id"); value != "" && value != uuidToString(taskID) {
			return &workflow.EngineError{Code: "debug_forbidden", Message: "upload task mismatch"}
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			return &workflow.EngineError{Code: "debug_bad_request", Message: "missing file"}
		}
		defer file.Close()
		data, err = io.ReadAll(file)
		if err != nil {
			return err
		}
		contentType := http.DetectContentType(data)
		if known, ok := extContentTypes[strings.ToLower(path.Ext(header.Filename))]; ok {
			contentType = known
		}
		objectID := uuid.New()
		attachmentID := pgtype.UUID{Bytes: objectID, Valid: true}
		key = "workspaces/" + uuidToString(run.WorkspaceID) + "/" + objectID.String() + path.Ext(header.Filename)
		// Store the deterministic object URL before any external write can start.
		row, err := q.CreateAttachment(ctx, db.CreateAttachmentParams{ID: attachmentID, WorkspaceID: run.WorkspaceID, TaskID: taskID, UploaderType: "agent", UploaderID: id.Task.AgentID, Filename: header.Filename, ContentType: contentType, SizeBytes: int64(len(data)), Url: h.Storage.ObjectURL(key)})
		if err != nil {
			return err
		}
		att = row.Attachment()
		upload, err = q.BeginWorkflowDebugUpload(ctx, db.BeginWorkflowDebugUploadParams{WorkspaceID: run.WorkspaceID, RunID: run.ID, ClaimID: claimID, TaskID: taskID})
		return err
	})
	if err != nil {
		h.writeDebugError(w, r, err)
		return true
	}
	if ignored != "" {
		writeJSON(w, 200, map[string]string{"status": ignored})
		return true
	}
	_, uploadErr := h.Storage.Upload(r.Context(), key, data, att.ContentType, att.Filename)
	// Even on request cancellation, persist the result after the storage call has
	// returned. A lost server process leaves the conservative pending intent.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), debugUploadSettleTimeout)
	defer cancel()
	err = engine.WithDebugTaskExecution(settleCtx, taskID, claimID, authenticate, func(ctx context.Context, q *db.Queries, id workflow.DebugTaskIdentity) error {
		if workflow.DebugPayloadDisposition(id, false) != "" {
			return &workflow.EngineError{Code: "debug_details_expired", Message: "upload delivery closed"}
		}
		state := "finalized"
		if uploadErr != nil {
			state = "aborted"
		}
		_, err := q.SettleWorkflowDebugUpload(ctx, db.SettleWorkflowDebugUploadParams{ID: upload.ID, WorkspaceID: run.WorkspaceID, State: state, AttachmentID: att.ID})
		return err
	})
	if err != nil {
		h.writeDebugError(w, r, err)
		return true
	}
	if uploadErr != nil {
		writeError(w, 503, "upload failed")
		return true
	}
	_ = user
	writeJSON(w, 200, h.attachmentToResponse(att, attachmentURLModeFromRequest(r)))
	return true
}
