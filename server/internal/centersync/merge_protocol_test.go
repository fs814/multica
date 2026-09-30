package centersync

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestContentThreeWayMerge(t *testing.T) {
	fields := func(v string) map[string]json.RawMessage { return map[string]json.RawMessage{"title": rawValue(v)} }
	for _, tc := range []struct {
		name                  string
		local, incoming, base map[string]json.RawMessage
		want                  string
		conflicts             int
	}{
		{"new", nil, fields("remote"), nil, "remote", 0},
		{"same", fields("same"), fields("same"), nil, "same", 0},
		{"remote changed", fields("old"), fields("new"), fields("old"), "new", 0},
		{"local changed", fields("new"), fields("old"), fields("old"), "new", 0},
		{"concurrent", fields("local"), fields("remote"), fields("old"), "local", 1},
		{"independent collision", fields("local"), fields("remote"), nil, "local", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			merged, _, conflicts := mergeFields(tc.local, tc.incoming, tc.base, "issue", "key")
			if textValue(merged["title"]) != tc.want || len(conflicts) != tc.conflicts {
				t.Fatalf("merged=%v conflicts=%v", merged, conflicts)
			}
		})
	}
}

func TestContentProjectionNeverSelectsCredentialOrExecutionColumns(t *testing.T) {
	for _, table := range contentTables {
		for _, field := range []string{"custom_env", "custom_args", "mcp_config", "runtime_config", "runtime_id", "permission_mode", "composio_toolkit_allowlist", "jwt_secret", "token", "password", "settings", "source_task_id"} {
			for _, allowed := range strings.Split(table.columns, ",") {
				if allowed == field {
					t.Fatalf("%s exposes %s", table.name, field)
				}
			}
		}
	}
	if canonicalUser("Owner@Example.test") != canonicalUser("owner@example.test") {
		t.Fatal("account mapping must be case insensitive")
	}
}

func TestContentMergeActorPairsAreAtomic(t *testing.T) {
	base := map[string]json.RawMessage{"assignee_type": rawValue("member"), "assignee_id": rawValue("one")}
	local := map[string]json.RawMessage{"assignee_type": rawValue("member"), "assignee_id": rawValue("two")}
	incoming := map[string]json.RawMessage{"assignee_type": rawValue("agent"), "assignee_id": rawValue("three")}
	merged, next, conflicts := mergeFields(local, incoming, base, "issue", "key")
	if !equalValue(rawValue(merged), rawValue(local)) || !equalValue(rawValue(next), rawValue(base)) || len(conflicts) != 1 {
		t.Fatal("actor identity and kind merged independently")
	}
}

func TestContentMergeRoutesRejectNonOwnerBeforeDatabase(t *testing.T) {
	h, _ := fixture(t)
	for _, action := range []string{"merge-list", "merge-export", "merge-apply", "merge-file-status", "merge-file-read", "merge-file-write"} {
		for _, actor := range []string{"missing", "other", "task_token", "cloud_pat"} {
			r := httptest.NewRequest("POST", Prefix+"/"+action, strings.NewReader("{}"))
			if actor == "other" {
				r.Header.Set("X-User-ID", uuid.NewString())
			} else if actor != "missing" {
				r.Header.Set("X-User-ID", h.config.Owner)
				r.Header.Set("X-Actor-Source", actor)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("%s %s: %d", action, actor, w.Code)
			}
		}
	}
}

func TestContentAttachmentsStayInsideUploadRootAndNeverClobber(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", dir)
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "https://center.example.test")
	t.Setenv("S3_BUCKET", "")
	if _, err := readAttachment("/uploads/absent"); !errors.Is(err, errAttachmentUnavailable) {
		t.Fatalf("missing file not classified: %v", err)
	}
	f := contentFile{uuid.NewString(), []byte("content")}
	address, err := writeAttachment(f)
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeAttachment(f)
	if err != nil || address != second {
		t.Fatal("content-addressed publication is not idempotent")
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("not an upload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/uploads/escape", "/uploads/../private.txt", "/uploads/key.meta.json", "/uploads/.staging.tmp", "https://user:password@example.test/uploads/key"} {
		if _, err := readAttachment(path); err == nil || errors.Is(err, errAttachmentUnavailable) {
			t.Fatalf("accepted unsafe attachment path %s", path)
		}
	}
	// A pre-existing file at the expected digest is verified, never overwritten.
	if err := os.WriteFile(filepath.Join(dir, "center-sync-"+contentHash(f.Data)), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeAttachment(f); err == nil {
		t.Fatal("accepted corrupt existing hash file")
	}
	t.Setenv("LOCAL_UPLOAD_DIR", filepath.Join(dir, "missing-root"))
	if _, err := readAttachment("/uploads/absent"); err == nil || errors.Is(err, errAttachmentUnavailable) {
		t.Fatal("a missing storage root must remain a configuration error")
	}
}

func TestUnavailableAttachmentValidation(t *testing.T) {
	workspace, attachment := uuid.NewString(), uuid.NewString()
	owner := canonicalUser("owner@example.test")
	fields := map[string]json.RawMessage{}
	table, _ := tableFor("workspace")
	for _, field := range strings.Split(table.columns, ",") {
		fields[field] = rawValue(nil)
	}
	fields["id"] = rawValue(workspace)
	bundle := contentBundle{Version: mergeVersion, Workspace: workspace, Owner: owner,
		Records: []contentRecord{{"workspace", fields}}, Users: []contentUser{{owner, "owner@example.test", "owner", "owner"}}, UnavailableAttachments: []string{attachment}}
	if err := validateBundle(bundle); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{{attachment, attachment}, {"bad-id"}, {""}} {
		bad := bundle
		bad.UnavailableAttachments = ids
		if validateBundle(bad) == nil {
			t.Fatal("invalid unavailable attachment accepted")
		}
	}
	bad := bundle
	bad.Files = []contentFile{{attachment, []byte("must not be present")}}
	if validateBundle(bad) == nil {
		t.Fatal("unavailable attachment also had bytes")
	}
	attachmentFields := map[string]json.RawMessage{}
	table, _ = tableFor("attachment")
	for _, field := range strings.Split(table.columns, ",") {
		attachmentFields[field] = rawValue(nil)
	}
	attachmentFields["id"] = rawValue(attachment)
	bad = bundle
	bad.Records = append(append([]contentRecord{}, bundle.Records...), contentRecord{"attachment", attachmentFields})
	if validateBundle(bad) == nil {
		t.Fatal("unavailable attachment also had a record")
	}
}
