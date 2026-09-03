package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type workflowCallbackClientFunc func(*http.Request) (*http.Response, error)

func (f workflowCallbackClientFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func seedWorkflowCallbackWorkerTest(t *testing.T) (*WorkflowCallbackWorker, db.WorkflowCallbackDelivery) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, "DELETE FROM workflow_callback_delivery WHERE workspace_id = $1", testWorkspaceID); err != nil {
		t.Fatalf("clear callback deliveries: %v", err)
	}

	box, err := secretbox.New(bytes.Repeat([]byte("c"), secretbox.KeySize))
	if err != nil {
		t.Fatalf("create callback secretbox: %v", err)
	}
	previousBox := testHandler.VCSSecretBox
	testHandler.VCSSecretBox = box
	t.Cleanup(func() { testHandler.VCSSecretBox = previousBox })
	sealed, err := box.Seal([]byte("callback-worker-test-secret"))
	if err != nil {
		t.Fatalf("seal callback secret: %v", err)
	}
	destination, err := testHandler.Queries.CreateWorkflowCallbackDestination(ctx, db.CreateWorkflowCallbackDestinationParams{
		WorkspaceID:            parseUUID(testWorkspaceID),
		Name:                   "callback worker edge " + time.Now().Format(time.RFC3339Nano),
		Url:                    "https://1.1.1.1/workflows",
		SigningSecretEncrypted: sealed,
		CreatedByUserID:        parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create callback destination: %v", err)
	}
	var runID string
	if err := testPool.QueryRow(ctx, "SELECT gen_random_uuid()").Scan(&runID); err != nil {
		t.Fatalf("allocate callback run id: %v", err)
	}
	delivery, err := testHandler.Queries.CreateWorkflowCallbackDelivery(ctx, db.CreateWorkflowCallbackDeliveryParams{
		WorkspaceID:   parseUUID(testWorkspaceID),
		DestinationID: destination.ID,
		WorkflowRunID: parseUUID(runID),
		EventType:     "run.completed",
		EventKey:      "callback-worker-edge-" + time.Now().Format(time.RFC3339Nano),
		Payload:       []byte("{\"event\":\"run.completed\"}"),
	})
	if err != nil {
		t.Fatalf("create callback delivery: %v", err)
	}
	if _, err := testPool.Exec(ctx, "UPDATE workflow_callback_delivery SET available_at = now() - interval '1 day' WHERE id = $1", delivery.ID); err != nil {
		t.Fatalf("make callback delivery available: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), "DELETE FROM workflow_callback_delivery WHERE destination_id = $1", destination.ID)
		_, _ = testPool.Exec(context.Background(), "DELETE FROM workflow_callback_destination WHERE id = $1", destination.ID)
	})
	return NewWorkflowCallbackWorker(testHandler), delivery
}

func TestSignWorkflowCallbackCoversTimestampAndExactPayload(t *testing.T) {
	secret := []byte("callback-signing-secret")
	timestamp := "1700000000"
	payload := []byte(`{"event":"run.completed","ok":true}`)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp + "."))
	mac.Write(payload)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := signWorkflowCallback(secret, timestamp, payload); got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
	if got := signWorkflowCallback(secret, timestamp, append(payload, ' ')); got == want {
		t.Fatal("signature did not change when the exact request body changed")
	}
	if got := signWorkflowCallback(secret, "1700000001", payload); got == want {
		t.Fatal("signature did not change when the timestamp changed")
	}
}

func TestValidateWorkflowCallbackURLRejectsUnsafeDestinations(t *testing.T) {
	tests := []string{
		"http://example.com/callback",
		"https://user:password@example.com/callback",
		"https://localhost/callback",
		"https://127.0.0.1/callback",
		"https://10.0.0.5/callback",
		"https://169.254.169.254/latest/meta-data",
		"https://100.64.0.1/callback",
		"https://192.0.2.1/callback",
		"https://198.18.0.1/callback",
		"https://198.51.100.1/callback",
		"https://203.0.113.1/callback",
		"https://240.0.0.1/callback",
		"https://[::1]/callback",
		"https://[2001:db8::1]/callback",
	}
	for _, raw := range tests {
		t.Run(strings.ReplaceAll(raw, "/", "_"), func(t *testing.T) {
			if err := validateWorkflowCallbackURL(context.Background(), raw); err == nil {
				t.Fatalf("unsafe callback URL %q was accepted", raw)
			}
		})
	}
}

func TestWorkflowCallbackDestinationRoleAndWorkspaceIsolation(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	box, err := secretbox.New(bytes.Repeat([]byte("d"), secretbox.KeySize))
	if err != nil {
		t.Fatalf("create callback secretbox: %v", err)
	}
	previousBox := testHandler.VCSSecretBox
	testHandler.VCSSecretBox = box
	t.Cleanup(func() { testHandler.VCSSecretBox = previousBox })

	memberID := createPlainMember(t, "workflow-callback-member-"+time.Now().Format("150405.000000000")+"@multica.test")
	create := httptest.NewRecorder()
	createReq := newRequestAs(memberID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/workflow-callback-destinations", map[string]any{
		"name":           "member cannot create",
		"url":            "https://1.1.1.1/callback",
		"signing_secret": "member-must-not-create-this",
	})
	createReq = withURLParam(createReq, "id", testWorkspaceID)
	testHandler.CreateWorkflowCallbackDestination(create, createReq)
	if create.Code != http.StatusForbidden {
		t.Fatalf("member destination create = %d, want 403: %s", create.Code, create.Body.String())
	}

	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")
	tpl := seededBugFixTemplate(t)
	foreignWorkspaceID := createOtherTestWorkspace(t)
	sealed, err := box.Seal([]byte("foreign-workspace-secret"))
	if err != nil {
		t.Fatalf("seal foreign callback secret: %v", err)
	}
	foreignDestination, err := testHandler.Queries.CreateWorkflowCallbackDestination(context.Background(), db.CreateWorkflowCallbackDestinationParams{
		WorkspaceID:            parseUUID(foreignWorkspaceID),
		Name:                   "foreign destination " + time.Now().Format(time.RFC3339Nano),
		Url:                    "https://1.1.1.1/callback",
		SigningSecretEncrypted: sealed,
		CreatedByUserID:        parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create foreign callback destination: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), "DELETE FROM workflow_callback_destination WHERE id = $1", foreignDestination.ID)
	})

	intake := httptest.NewRecorder()
	intakeReq := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/workflow-intake", map[string]any{
		"source":                  "callback-isolation",
		"event_id":                "foreign-destination",
		"template_key":            tpl.Key,
		"title":                   "Reject foreign callback",
		"callback_destination_id": uuidToString(foreignDestination.ID),
	}), "id", testWorkspaceID)
	testHandler.WorkflowIntake(intake, intakeReq)
	if intake.Code != http.StatusUnprocessableEntity {
		t.Fatalf("foreign destination intake = %d, want 422: %s", intake.Code, intake.Body.String())
	}
}

func TestValidateWorkflowWebhookPayloadPinsTemplate(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "matching direct key", body: `{"template_key":"incident-response","title":"alarm"}`},
		{name: "matching envelope key", body: `{"event":"alert","eventPayload":{"template_key":"incident-response"}}`},
		{name: "conflicting direct key", body: `{"template_key":"other"}`, wantErr: true},
		{name: "conflicting envelope key", body: `{"event":"alert","eventPayload":{"template_key":"other"}}`, wantErr: true},
		{name: "non string key", body: `{"template_key":42}`, wantErr: true},
		{name: "array payload", body: `[]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkflowWebhookPayload([]byte(tc.body), "incident-response")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestWorkflowCallbackRetriesRateLimitAndNetworkErrors(t *testing.T) {
	tests := []struct {
		name   string
		client workflowCallbackHTTPClient
	}{
		{
			name: "http 429",
			client: workflowCallbackClientFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader("slow down")), Header: make(http.Header)}, nil
			}),
		},
		{
			name: "network error",
			client: workflowCallbackClientFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset")
			}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			worker, delivery := seedWorkflowCallbackWorkerTest(t)
			worker.client = tc.client
			worked, err := worker.ProcessNext(context.Background())
			if err != nil || !worked {
				t.Fatalf("ProcessNext = (%v, %v), want (true, nil)", worked, err)
			}
			got, err := testHandler.Queries.GetWorkflowCallbackDelivery(context.Background(), db.GetWorkflowCallbackDeliveryParams{
				ID: delivery.ID, WorkspaceID: parseUUID(testWorkspaceID),
			})
			if err != nil || got.Status != "queued" || got.AttemptCount != 1 || !got.Error.Valid {
				t.Fatalf("retry state: status=%q attempts=%d error=%v query_err=%v", got.Status, got.AttemptCount, got.Error, err)
			}
		})
	}
}

func TestWorkflowCallbackMaxAttemptsBecomesPermanentFailure(t *testing.T) {
	worker, delivery := seedWorkflowCallbackWorkerTest(t)
	worker.client = workflowCallbackClientFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network remains unavailable")
	})
	if _, err := testPool.Exec(context.Background(), "UPDATE workflow_callback_delivery SET attempt_count = $2 WHERE id = $1", delivery.ID, workflowCallbackMaxAttempts-1); err != nil {
		t.Fatalf("seed attempt count: %v", err)
	}
	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("ProcessNext = (%v, %v), want (true, nil)", worked, err)
	}
	got, err := testHandler.Queries.GetWorkflowCallbackDelivery(context.Background(), db.GetWorkflowCallbackDeliveryParams{
		ID: delivery.ID, WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil || got.Status != "failed" || got.AttemptCount != workflowCallbackMaxAttempts {
		t.Fatalf("max-attempt state: status=%q attempts=%d query_err=%v", got.Status, got.AttemptCount, err)
	}
}

func TestWorkflowCallbackExpiredLeaseRecoversClaimedDelivery(t *testing.T) {
	worker, delivery := seedWorkflowCallbackWorkerTest(t)
	claimed, err := testHandler.Queries.ClaimQueuedWorkflowCallbackDelivery(context.Background())
	if err != nil || claimed.ID != delivery.ID {
		t.Fatalf("initial claim: id=%v err=%v", claimed.ID, err)
	}
	if _, err := testHandler.Queries.ClaimQueuedWorkflowCallbackDelivery(context.Background()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second claim while leased error = %v, want no rows", err)
	}
	if _, err := testPool.Exec(context.Background(), "UPDATE workflow_callback_delivery SET lease_expires_at = now() - interval '1 second' WHERE id = $1", delivery.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	reclaimed, err := testHandler.Queries.ReclaimExpiredWorkflowCallbackDeliveries(context.Background())
	if err != nil || reclaimed != 1 {
		t.Fatalf("reclaim expired delivery = (%d, %v), want (1, nil)", reclaimed, err)
	}
	worker.client = workflowCallbackClientFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("recovered ProcessNext = (%v, %v), want (true, nil)", worked, err)
	}
	got, err := testHandler.Queries.GetWorkflowCallbackDelivery(context.Background(), db.GetWorkflowCallbackDeliveryParams{
		ID: delivery.ID, WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil || got.Status != "delivered" || got.AttemptCount != 2 {
		t.Fatalf("recovered state: status=%q attempts=%d query_err=%v", got.Status, got.AttemptCount, err)
	}
}

func TestWorkflowCallbackConcurrentWorkersDeliverOnce(t *testing.T) {
	first, delivery := seedWorkflowCallbackWorkerTest(t)
	second := NewWorkflowCallbackWorker(testHandler)
	var requests atomic.Int32
	client := workflowCallbackClientFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	first.client = client
	second.client = client

	type result struct {
		worked bool
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, worker := range []*WorkflowCallbackWorker{first, second} {
		wg.Add(1)
		go func(w *WorkflowCallbackWorker) {
			defer wg.Done()
			worked, err := w.ProcessNext(context.Background())
			results <- result{worked: worked, err: err}
		}(worker)
	}
	wg.Wait()
	close(results)
	workedCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent ProcessNext: %v", result.err)
		}
		if result.worked {
			workedCount++
		}
	}
	if workedCount != 1 || requests.Load() != 1 {
		t.Fatalf("concurrent workers: worked=%d requests=%d, want one claim and one request", workedCount, requests.Load())
	}
	got, err := testHandler.Queries.GetWorkflowCallbackDelivery(context.Background(), db.GetWorkflowCallbackDeliveryParams{
		ID: delivery.ID, WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil || got.Status != "delivered" || got.AttemptCount != 1 {
		t.Fatalf("concurrent delivery state: status=%q attempts=%d query_err=%v", got.Status, got.AttemptCount, err)
	}
}

func TestWorkflowCallbackDeliverySignsRetriesAndReplays(t *testing.T) {
	cleanupWorkflowTemplates(t)
	cleanupWorkflowRuns(t)
	withWorkflowEngineForTest(t)
	labelTestAgentWithCapabilities(t, "bug_analysis", "code_change")
	tpl := seededBugFixTemplate(t)

	previousBox := testHandler.VCSSecretBox
	box, err := secretbox.New(make([]byte, secretbox.KeySize))
	if err != nil {
		t.Fatalf("create callback secretbox: %v", err)
	}
	testHandler.VCSSecretBox = box
	t.Cleanup(func() { testHandler.VCSSecretBox = previousBox })
	secret := []byte("callback-secret-for-integration")
	sealed, err := box.Seal(secret)
	if err != nil {
		t.Fatalf("seal callback secret: %v", err)
	}
	destination, err := testHandler.Queries.CreateWorkflowCallbackDestination(context.Background(), db.CreateWorkflowCallbackDestinationParams{
		WorkspaceID: parseUUID(testWorkspaceID), Name: "callback worker " + time.Now().Format(time.RFC3339Nano),
		Url: "https://1.1.1.1/workflows", SigningSecretEncrypted: sealed, CreatedByUserID: parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create callback destination: %v", err)
	}
	realtimeEvents := make(chan events.Event, 32)
	testHandler.Bus.Subscribe(protocol.EventWorkflowEvent, func(event events.Event) {
		realtimeEvents <- event
	})

	intake := httptest.NewRecorder()
	intakeReq := withURLParam(newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/workflow-intake", map[string]any{
		"source": "callback-test", "event_id": "callback-1", "template_key": tpl.Key,
		"title": "Deliver callback", "description": "Exercise callback signing, retry, and replay.",
		"callback_destination_id": uuidToString(destination.ID),
	}), "id", testWorkspaceID)
	testHandler.WorkflowIntake(intake, intakeReq)
	if intake.Code != http.StatusCreated {
		t.Fatalf("workflow intake = %d %s", intake.Code, intake.Body.String())
	}
	var receipt WorkflowIntakeResponse
	_ = json.Unmarshal(intake.Body.Bytes(), &receipt)
	rows, err := testHandler.Queries.ListWorkflowCallbackDeliveriesForRun(context.Background(), db.ListWorkflowCallbackDeliveriesForRunParams{
		WorkflowRunID: parseUUID(receipt.WorkflowRunID), WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list queued callbacks: rows=%d err=%v", len(rows), err)
	}
	delivery := rows[0]
	var callbackEnvelope workflow.EventEnvelope
	if err := json.Unmarshal(delivery.Payload, &callbackEnvelope); err != nil {
		t.Fatalf("decode callback event envelope: %v", err)
	}
	if callbackEnvelope.SchemaVersion != "1" || callbackEnvelope.EventID != delivery.EventKey || callbackEnvelope.EventType != delivery.EventType {
		t.Fatalf("callback envelope does not carry durable identity: envelope=%+v delivery=%+v", callbackEnvelope, delivery)
	}
	foundRealtimeIdentity := false
	var realtimeIDs []string
	for len(realtimeEvents) > 0 {
		event := <-realtimeEvents
		envelope, ok := event.Payload.(workflow.EventEnvelope)
		if ok {
			realtimeIDs = append(realtimeIDs, envelope.EventID)
		}
		if ok && envelope.EventID == delivery.EventKey {
			foundRealtimeIdentity = true
			break
		}
	}
	if !foundRealtimeIdentity {
		t.Fatalf("realtime stream IDs %v did not include callback event_id %s", realtimeIDs, delivery.EventKey)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE workflow_callback_delivery SET available_at = now() - interval '1 day' WHERE id = $1`, delivery.ID); err != nil {
		t.Fatalf("prioritize callback delivery: %v", err)
	}

	statuses := []int{http.StatusInternalServerError, http.StatusNoContent, http.StatusNoContent, http.StatusBadRequest}
	requestCount := 0
	var capturedBody []byte
	var capturedSignature, capturedTimestamp, capturedDelivery string
	worker := NewWorkflowCallbackWorker(testHandler)
	worker.client = workflowCallbackClientFunc(func(req *http.Request) (*http.Response, error) {
		capturedBody, _ = io.ReadAll(req.Body)
		capturedSignature = req.Header.Get("X-Multica-Signature-256")
		capturedTimestamp = req.Header.Get("X-Multica-Timestamp")
		capturedDelivery = req.Header.Get("X-Multica-Delivery")
		status := statuses[requestCount]
		requestCount++
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("callback response")), Header: make(http.Header)}, nil
	})

	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("first callback attempt: worked=%v err=%v", worked, err)
	}
	queued, err := testHandler.Queries.GetWorkflowCallbackDelivery(context.Background(), db.GetWorkflowCallbackDeliveryParams{ID: delivery.ID, WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil || queued.Status != "queued" || queued.AttemptCount != 1 {
		t.Fatalf("retry state: status=%q attempts=%d err=%v", queued.Status, queued.AttemptCount, err)
	}
	if !bytes.Equal(capturedBody, delivery.Payload) || capturedDelivery != uuidToString(delivery.ID) || capturedSignature != signWorkflowCallback(secret, capturedTimestamp, capturedBody) {
		t.Fatalf("callback request was not signed over its exact body: delivery=%q signature=%q timestamp=%q", capturedDelivery, capturedSignature, capturedTimestamp)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE workflow_callback_delivery SET available_at = now() - interval '1 second' WHERE id = $1`, delivery.ID); err != nil {
		t.Fatalf("make retry available: %v", err)
	}
	worked, err = worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("second callback attempt: worked=%v err=%v", worked, err)
	}
	delivered, err := testHandler.Queries.GetWorkflowCallbackDelivery(context.Background(), db.GetWorkflowCallbackDeliveryParams{ID: delivery.ID, WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil || delivered.Status != "delivered" || delivered.AttemptCount != 2 || !delivered.DeliveredAt.Valid {
		t.Fatalf("delivered state: status=%q attempts=%d delivered=%v err=%v", delivered.Status, delivered.AttemptCount, delivered.DeliveredAt.Valid, err)
	}

	replay := httptest.NewRecorder()
	replayReq := withURLParam(newRequest("POST", "/api/workflow-callback-deliveries/"+uuidToString(delivery.ID)+"/replay?workspace_id="+testWorkspaceID, nil), "id", uuidToString(delivery.ID))
	testHandler.ReplayWorkflowCallbackDelivery(replay, replayReq)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay callback = %d %s", replay.Code, replay.Body.String())
	}
	replayed, err := testHandler.Queries.GetWorkflowCallbackDelivery(context.Background(), db.GetWorkflowCallbackDeliveryParams{ID: delivery.ID, WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil || replayed.Status != "queued" || replayed.AttemptCount != 0 || replayed.WorkflowRunID != delivery.WorkflowRunID {
		t.Fatalf("replay state: status=%q attempts=%d run=%v err=%v", replayed.Status, replayed.AttemptCount, replayed.WorkflowRunID, err)
	}
	worked, err = worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("replayed callback attempt: worked=%v err=%v", worked, err)
	}
	if requestCount != 3 {
		t.Fatalf("callback request count = %d, want initial + retry + replay", requestCount)
	}

	permanent, err := testHandler.Queries.CreateWorkflowCallbackDelivery(context.Background(), db.CreateWorkflowCallbackDeliveryParams{
		WorkspaceID: parseUUID(testWorkspaceID), DestinationID: destination.ID, WorkflowRunID: parseUUID(receipt.WorkflowRunID),
		EventType: "run.failed", EventKey: "callback-test:permanent-failure", Payload: []byte(`{"event":"run.failed"}`),
	})
	if err != nil {
		t.Fatalf("create permanent-failure callback: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE workflow_callback_delivery SET available_at = now() - interval '1 second' WHERE id = $1`, permanent.ID); err != nil {
		t.Fatalf("make permanent callback available: %v", err)
	}
	worked, err = worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("permanent callback attempt: worked=%v err=%v", worked, err)
	}
	failed, err := testHandler.Queries.GetWorkflowCallbackDelivery(context.Background(), db.GetWorkflowCallbackDeliveryParams{ID: permanent.ID, WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil || failed.Status != "failed" || failed.ResponseStatus.Int32 != http.StatusBadRequest {
		t.Fatalf("permanent failure state: status=%q response=%v err=%v", failed.Status, failed.ResponseStatus, err)
	}
	var inboxCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM inbox_item
		WHERE workspace_id = $1 AND issue_id = $2 AND type = 'workflow_callback_failed'`, testWorkspaceID, receipt.IssueID).Scan(&inboxCount); err != nil {
		t.Fatalf("count callback failure inbox items: %v", err)
	}
	if inboxCount != 1 {
		t.Fatalf("callback failure inbox items = %d, want one owner notification", inboxCount)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE workspace_id = $1 AND issue_id = $2 AND type = 'workflow_callback_failed'`, testWorkspaceID, receipt.IssueID)
	})
}
