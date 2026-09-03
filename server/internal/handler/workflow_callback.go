package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	workflowCallbackMaxAttempts   = 6
	workflowCallbackConcurrency   = 4
	workflowCallbackResponseLimit = 4096
)

var workflowCallbackReservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type workflowCallbackHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type WorkflowCallbackWorker struct {
	h      *Handler
	client workflowCallbackHTTPClient
	notify chan struct{}
	done   chan struct{}
}

func NewWorkflowCallbackWorker(h *Handler) *WorkflowCallbackWorker {
	return &WorkflowCallbackWorker{
		h:      h,
		client: newWorkflowCallbackHTTPClient(),
		notify: make(chan struct{}, workflowCallbackConcurrency),
		done:   make(chan struct{}),
	}
}

func newWorkflowCallbackHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid callback address: %w", err)
		}
		ips, err := resolveSafeWorkflowCallbackIPs(ctx, host)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		var lastErr error
		for _, ip := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, fmt.Errorf("connect callback destination: %w", lastErr)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("workflow callback redirects are disabled")
		},
	}
}

func (w *WorkflowCallbackWorker) Notify() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *WorkflowCallbackWorker) Run(ctx context.Context) {
	if w == nil || w.h == nil || w.h.Queries == nil {
		return
	}
	defer close(w.done)
	var workers sync.WaitGroup
	workers.Add(workflowCallbackConcurrency)
	for range workflowCallbackConcurrency {
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				worked, err := w.ProcessNext(ctx)
				if err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("workflow callback delivery failed", "error", err)
				}
				if worked {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-w.notify:
				case <-ticker.C:
					_, _ = w.h.Queries.ReclaimExpiredWorkflowCallbackDeliveries(ctx)
				}
			}
		}()
	}
	workers.Wait()
}

func (w *WorkflowCallbackWorker) ProcessNext(ctx context.Context) (bool, error) {
	defer w.sampleBacklogMetric(ctx)
	delivery, err := w.h.Queries.ClaimQueuedWorkflowCallbackDelivery(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim workflow callback: %w", err)
	}
	destination, err := w.h.Queries.GetWorkflowCallbackDestination(ctx, db.GetWorkflowCallbackDestinationParams{ID: delivery.DestinationID, WorkspaceID: delivery.WorkspaceID})
	if err != nil || !destination.Enabled {
		return true, w.complete(ctx, delivery, "failed", 0, "", "callback destination is unavailable")
	}
	if err := validateWorkflowCallbackURL(ctx, destination.Url); err != nil {
		return true, w.complete(ctx, delivery, "failed", 0, "", err.Error())
	}
	if w.h.VCSSecretBox == nil {
		return true, w.retryOrFail(ctx, delivery, errors.New("workflow callback encryption key is unavailable"))
	}
	secret, err := w.h.VCSSecretBox.Open(destination.SigningSecretEncrypted)
	if err != nil {
		return true, w.complete(ctx, delivery, "failed", 0, "", "callback signing secret cannot be decrypted")
	}
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signature := signWorkflowCallback(secret, timestamp, delivery.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination.Url, bytes.NewReader(delivery.Payload))
	if err != nil {
		return true, w.complete(ctx, delivery, "failed", 0, "", err.Error())
	}
	return w.sendRequest(ctx, delivery, req, timestamp, signature)
}

func signWorkflowCallback(secret []byte, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (w *WorkflowCallbackWorker) sendRequest(ctx context.Context, delivery db.WorkflowCallbackDelivery, req *http.Request, timestamp, signature string) (bool, error) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Multica-Timestamp", timestamp)
	req.Header.Set("X-Multica-Signature-256", signature)
	req.Header.Set("X-Multica-Delivery", uuidToString(delivery.ID))

	resp, err := w.client.Do(req)
	if err != nil {
		return true, w.retryOrFail(ctx, delivery, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, workflowCallbackResponseLimit))
	bodyText := string(body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, w.complete(ctx, delivery, "delivered", resp.StatusCode, bodyText, "")
	}
	cause := fmt.Errorf("callback returned HTTP %d", resp.StatusCode)
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return true, w.retryOrFailWithResponse(ctx, delivery, cause, resp.StatusCode, bodyText)
	}
	return true, w.complete(ctx, delivery, "failed", resp.StatusCode, bodyText, cause.Error())
}

func (w *WorkflowCallbackWorker) retryOrFail(ctx context.Context, delivery db.WorkflowCallbackDelivery, cause error) error {
	return w.retryOrFailWithResponse(ctx, delivery, cause, 0, "")
}

func (w *WorkflowCallbackWorker) retryOrFailWithResponse(ctx context.Context, delivery db.WorkflowCallbackDelivery, cause error, status int, body string) error {
	if delivery.AttemptCount >= workflowCallbackMaxAttempts {
		return w.complete(ctx, delivery, "failed", status, body, cause.Error())
	}
	backoff := time.Second * time.Duration(1<<min(delivery.AttemptCount-1, 6))
	_, err := w.h.Queries.RetryClaimedWorkflowCallbackDelivery(ctx, db.RetryClaimedWorkflowCallbackDeliveryParams{
		ID: delivery.ID, LeaseToken: delivery.LeaseToken,
		AvailableAt: pgtype.Timestamptz{Time: time.Now().Add(backoff), Valid: true},
		Error:       pgtype.Text{String: cause.Error(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil && w.h.WorkflowEngine != nil && w.h.WorkflowEngine.Metrics != nil {
		w.h.WorkflowEngine.Metrics.RecordCallback("retry")
	}
	return err
}

func (w *WorkflowCallbackWorker) complete(ctx context.Context, delivery db.WorkflowCallbackDelivery, status string, responseStatus int, responseBody, message string) error {
	params := db.CompleteClaimedWorkflowCallbackDeliveryParams{
		ID: delivery.ID, LeaseToken: delivery.LeaseToken, Status: status,
		ResponseBody: pgtype.Text{String: responseBody, Valid: responseBody != ""},
		Error:        pgtype.Text{String: message, Valid: message != ""},
	}
	if responseStatus != 0 {
		params.ResponseStatus = pgtype.Int4{Int32: int32(responseStatus), Valid: true}
	}
	_, err := w.h.Queries.CompleteClaimedWorkflowCallbackDelivery(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil && status == "failed" {
		w.notifyPermanentFailure(ctx, delivery, message)
	}
	if err == nil && w.h.WorkflowEngine != nil && w.h.WorkflowEngine.Metrics != nil {
		w.h.WorkflowEngine.Metrics.RecordCallback(status)
	}
	return err
}

func (w *WorkflowCallbackWorker) sampleBacklogMetric(ctx context.Context) {
	if w == nil || w.h == nil || w.h.WorkflowEngine == nil || w.h.WorkflowEngine.Metrics == nil {
		return
	}
	seconds, err := w.h.Queries.OldestQueuedWorkflowCallbackSeconds(ctx)
	if err == nil {
		w.h.WorkflowEngine.Metrics.SetOldestQueuedCallback(seconds)
	}
}

func (w *WorkflowCallbackWorker) notifyPermanentFailure(ctx context.Context, delivery db.WorkflowCallbackDelivery, message string) {
	run, err := w.h.Queries.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: delivery.WorkflowRunID, WorkspaceID: delivery.WorkspaceID})
	if err != nil || !run.AccountableUserID.Valid {
		return
	}
	details, _ := json.Marshal(map[string]any{"workflow_run_id": uuidToString(run.ID), "callback_delivery_id": uuidToString(delivery.ID)})
	_, _ = w.h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID: delivery.WorkspaceID, RecipientType: "member", RecipientID: run.AccountableUserID,
		Type: "workflow_callback_failed", Severity: "attention", IssueID: run.IssueID,
		Title: "Workflow callback delivery failed", Body: pgtype.Text{String: message, Valid: message != ""},
		ActorType: pgtype.Text{String: "system", Valid: true}, Details: details,
	})
}

type workflowCallbackDestinationRequest struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	SigningSecret string `json:"signing_secret"`
}

type workflowCallbackDestinationResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
}

func callbackDestinationResponse(row db.WorkflowCallbackDestination) workflowCallbackDestinationResponse {
	return workflowCallbackDestinationResponse{ID: uuidToString(row.ID), WorkspaceID: uuidToString(row.WorkspaceID), Name: row.Name, URL: row.Url, Enabled: row.Enabled, CreatedAt: timestampToString(row.CreatedAt)}
}

func (h *Handler) CreateWorkflowCallbackDestination(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}
	if h.VCSSecretBox == nil {
		writeError(w, http.StatusServiceUnavailable, "callback secret encryption is not configured")
		return
	}
	var req workflowCallbackDestinationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	req.SigningSecret = strings.TrimSpace(req.SigningSecret)
	if req.Name == "" || len(req.Name) > 100 || len(req.SigningSecret) < 16 {
		writeError(w, http.StatusBadRequest, "name and a signing_secret of at least 16 characters are required")
		return
	}
	if err := validateWorkflowCallbackURL(r.Context(), req.URL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sealed, err := h.VCSSecretBox.Seal([]byte(req.SigningSecret))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt callback secret")
		return
	}
	row, err := h.Queries.CreateWorkflowCallbackDestination(r.Context(), db.CreateWorkflowCallbackDestinationParams{
		WorkspaceID: wsUUID, Name: req.Name, Url: req.URL,
		SigningSecretEncrypted: sealed, CreatedByUserID: member.UserID,
	})
	if err != nil {
		writeError(w, http.StatusConflict, "callback destination name already exists")
		return
	}
	writeJSON(w, http.StatusCreated, callbackDestinationResponse(row))
}

func (h *Handler) ListWorkflowCallbackDestinations(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	rows, err := h.Queries.ListWorkflowCallbackDestinations(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list callback destinations")
		return
	}
	out := make([]workflowCallbackDestinationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, callbackDestinationResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"destinations": out})
}

func (h *Handler) ReplayWorkflowCallbackDelivery(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	deliveryID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "delivery id")
	if !ok {
		return
	}
	row, err := h.Queries.ReplayWorkflowCallbackDelivery(r.Context(), db.ReplayWorkflowCallbackDeliveryParams{ID: deliveryID, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusConflict, "callback delivery is not replayable")
		return
	}
	if h.WorkflowCallbackWorker != nil {
		h.WorkflowCallbackWorker.Notify()
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": uuidToString(row.ID), "status": row.Status, "workflow_run_id": uuidToString(row.WorkflowRunID), "event_key": row.EventKey})
}

type workflowCallbackDeliveryResponse struct {
	ID             string  `json:"id"`
	DestinationID  string  `json:"destination_id"`
	WorkflowRunID  string  `json:"workflow_run_id"`
	EventType      string  `json:"event_type"`
	EventKey       string  `json:"event_key"`
	Status         string  `json:"status"`
	AttemptCount   int32   `json:"attempt_count"`
	ResponseStatus *int32  `json:"response_status"`
	Error          *string `json:"error"`
	AvailableAt    string  `json:"available_at"`
	DeliveredAt    *string `json:"delivered_at"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

func workflowCallbackDeliveryToResponse(row db.WorkflowCallbackDelivery) workflowCallbackDeliveryResponse {
	return workflowCallbackDeliveryResponse{
		ID: uuidToString(row.ID), DestinationID: uuidToString(row.DestinationID), WorkflowRunID: uuidToString(row.WorkflowRunID),
		EventType: row.EventType, EventKey: row.EventKey, Status: row.Status, AttemptCount: row.AttemptCount,
		ResponseStatus: int4ToPtr(row.ResponseStatus), Error: textToPtr(row.Error), AvailableAt: timestampToString(row.AvailableAt),
		DeliveredAt: timestampToPtr(row.DeliveredAt), CreatedAt: timestampToString(row.CreatedAt), UpdatedAt: timestampToString(row.UpdatedAt),
	}
}

func (h *Handler) ListWorkflowRunCallbackDeliveries(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workflow run id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetWorkflowRun(r.Context(), db.GetWorkflowRunParams{ID: runID, WorkspaceID: wsUUID}); err != nil {
		writeError(w, http.StatusNotFound, "workflow run not found")
		return
	}
	rows, err := h.Queries.ListWorkflowCallbackDeliveriesForRun(r.Context(), db.ListWorkflowCallbackDeliveriesForRunParams{WorkflowRunID: runID, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list callback deliveries")
		return
	}
	out := make([]workflowCallbackDeliveryResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, workflowCallbackDeliveryToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": out})
}

func validateWorkflowCallbackURL(ctx context.Context, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("callback URL must be an absolute https URL without user info")
	}
	_, err = resolveSafeWorkflowCallbackIPs(ctx, parsed.Hostname())
	return err
}

func resolveSafeWorkflowCallbackIPs(ctx context.Context, rawHost string) ([]net.IP, error) {
	host := strings.ToLower(strings.TrimSuffix(rawHost, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, errors.New("callback URL resolves to a forbidden host")
	}
	ips := []net.IP{}
	if literal := net.ParseIP(host); literal != nil {
		ips = append(ips, literal)
	} else {
		resolveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resolved, err := net.DefaultResolver.LookupIP(resolveCtx, "ip", host)
		if err != nil || len(resolved) == 0 {
			return nil, errors.New("callback URL host could not be resolved")
		}
		ips = resolved
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || isReservedWorkflowCallbackIP(ip) {
			return nil, errors.New("callback URL resolves to a private or local address")
		}
	}
	return ips, nil
}

func isReservedWorkflowCallbackIP(ip net.IP) bool {
	addr, err := netip.ParseAddr(ip.String())
	if err != nil {
		return true
	}
	addr = addr.Unmap()
	for _, prefix := range workflowCallbackReservedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
