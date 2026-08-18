package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var workflowIntakeSourcePattern = regexp.MustCompile("^[a-z0-9][a-z0-9._-]{0,63}$")

type WorkflowIntakeRequest struct {
	Source                string          `json:"source"`
	EventID               string          `json:"event_id"`
	TemplateKey           string          `json:"template_key"`
	Title                 string          `json:"title"`
	Description           string          `json:"description"`
	OwnerUserID           string          `json:"owner_user_id"`
	SourceURL             string          `json:"source_url"`
	Payload               json.RawMessage `json:"payload"`
	CallbackDestinationID *string         `json:"callback_destination_id"`
}

type WorkflowIntakeResponse struct {
	ReceiptID     string `json:"receipt_id"`
	WorkflowRunID string `json:"workflow_run_id"`
	IssueID       string `json:"issue_id"`
	Status        string `json:"status"`
	TemplateKey   string `json:"template_key"`
}

func (h *Handler) WorkflowIntake(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	engine, ok := h.requireWorkflowEngine(w)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, workflowRunBodyLimit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var req WorkflowIntakeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Source = strings.ToLower(strings.TrimSpace(req.Source))
	req.EventID = strings.TrimSpace(req.EventID)
	req.TemplateKey = strings.TrimSpace(req.TemplateKey)
	req.Title = sanitizeNullBytes(strings.TrimSpace(req.Title))
	req.Description = sanitizeNullBytes(strings.TrimSpace(req.Description))
	req.SourceURL = strings.TrimSpace(req.SourceURL)
	if !workflowIntakeSourcePattern.MatchString(req.Source) {
		writeError(w, http.StatusUnprocessableEntity, "source must match [a-z0-9][a-z0-9._-]{0,63}")
		return
	}
	if req.EventID == "" || len(req.EventID) > 256 || req.TemplateKey == "" {
		writeError(w, http.StatusUnprocessableEntity, "event_id and template_key are required")
		return
	}
	if req.Title == "" || utf8.RuneCountInString(req.Title) > maxWorkflowRunTitleLen ||
		req.Description == "" || utf8.RuneCountInString(req.Description) > maxWorkflowRunDescriptionLen {
		writeError(w, http.StatusUnprocessableEntity, "title and description are required and must be within workflow limits")
		return
	}
	if req.SourceURL != "" {
		parsed, err := url.ParseRequestURI(req.SourceURL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			writeError(w, http.StatusUnprocessableEntity, "source_url must be an http or https URL")
			return
		}
	}

	template, err := h.Queries.GetWorkflowTemplateByKey(r.Context(), db.GetWorkflowTemplateByKeyParams{WorkspaceID: wsUUID, Key: req.TemplateKey})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workflow template not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to resolve workflow template")
		}
		return
	}
	if template.Status != "published" || !template.CurrentVersion.Valid {
		writeError(w, http.StatusConflict, "workflow template is not published")
		return
	}

	ownerID := member.UserID
	if req.OwnerUserID != "" {
		parsed, ok := parseUUIDOrBadRequest(w, req.OwnerUserID, "owner_user_id")
		if !ok {
			return
		}
		if parsed != member.UserID && member.Role != "owner" && member.Role != "admin" {
			writeError(w, http.StatusForbidden, "only an owner or admin may assign external intake to another member")
			return
		}
		if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: parsed, WorkspaceID: wsUUID}); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "owner_user_id is not a member of this workspace")
			return
		}
		ownerID = parsed
	}

	var callbackID pgtype.UUID
	if req.CallbackDestinationID != nil && strings.TrimSpace(*req.CallbackDestinationID) != "" {
		parsed, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(*req.CallbackDestinationID), "callback_destination_id")
		if !ok {
			return
		}
		destination, err := h.Queries.GetWorkflowCallbackDestination(r.Context(), db.GetWorkflowCallbackDestinationParams{ID: parsed, WorkspaceID: wsUUID})
		if err != nil || !destination.Enabled {
			writeError(w, http.StatusUnprocessableEntity, "callback destination not found or disabled")
			return
		}
		callbackID = parsed
	}

	payloadBag := map[string]any{}
	if len(req.Payload) > 0 && string(req.Payload) != "null" {
		if err := json.Unmarshal(req.Payload, &payloadBag); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "payload must be a JSON object")
			return
		}
	}
	payloadBag["title"] = req.Title
	payloadBag["description"] = req.Description
	payloadBag["source_url"] = req.SourceURL
	input, _ := json.Marshal(payloadBag)
	hashBody, _ := json.Marshal(map[string]any{
		"source": req.Source, "event_id": req.EventID, "template_key": strings.ToLower(template.Key),
		"title": req.Title, "description": req.Description, "owner_user_id": uuidToString(ownerID),
		"source_url": req.SourceURL, "payload": payloadBag, "callback_destination_id": uuidToString(callbackID),
	})
	requestHashBytes := sha256.Sum256(hashBody)
	requestHash := hex.EncodeToString(requestHashBytes[:])
	eventHash := sha256.Sum256([]byte(req.EventID))
	idempotencyKey := "external:" + req.Source + ":" + hex.EncodeToString(eventHash[:])

	started, err := engine.StartRun(r.Context(), workflow.StartRunInput{
		WorkspaceID:           wsUUID,
		TemplateID:            template.ID,
		Source:                "external",
		SourceEventID:         req.EventID,
		IdempotencyKey:        idempotencyKey,
		RequestHash:           requestHash,
		CallbackDestinationID: callbackID,
		AccountableUserID:     ownerID,
		Issue: &workflow.RunIssueInput{
			Title:       req.Title,
			Description: req.Description,
			CreatorType: "member",
			CreatorID:   member.UserID,
			Subscribers: []workflow.RunIssueSubscriber{{UserType: "member", UserID: ownerID, Reason: "manual"}},
		},
		Input:     input,
		ActorType: "member",
		ActorID:   member.UserID,
	})
	if err != nil {
		h.writeWorkflowEngineError(w, r, err, "WorkflowIntake")
		return
	}
	run := h.reloadWorkflowRun(r.Context(), started.Run)
	if !started.AlreadyExisted && started.Issue != nil {
		prefix := h.getIssuePrefix(r.Context(), wsUUID)
		h.publish(protocol.EventIssueCreated, workspaceID, "member", uuidToString(member.UserID), map[string]any{"issue": issueToResponse(*started.Issue, prefix)})
	}
	status := http.StatusCreated
	if started.AlreadyExisted {
		status = http.StatusOK
	}
	writeJSON(w, status, WorkflowIntakeResponse{
		ReceiptID: uuidToString(run.ID), WorkflowRunID: uuidToString(run.ID),
		IssueID: uuidToString(run.IssueID), Status: run.Status, TemplateKey: template.Key,
	})
}
