package centersync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

func TestAttachmentChunkValidationBeforeDatabase(t *testing.T) {
	h, _ := fixture(t)
	valid := attachmentChunkInput{Workspace: uuid.NewString(), Peer: "https://source.example.test", Attachment: uuid.NewString(), Hash: strings.Repeat("a", 64), Size: 8, Offset: 0}
	for _, change := range []func(*attachmentChunkInput){
		func(in *attachmentChunkInput) { in.Workspace = "invalid" },
		func(in *attachmentChunkInput) { in.Hash = "../escape" },
		func(in *attachmentChunkInput) { in.Size = maxAttachmentBytes + 1 },
		func(in *attachmentChunkInput) { in.Offset = -1 },
		func(in *attachmentChunkInput) { in.Offset = 9 },
		func(in *attachmentChunkInput) { in.Peer = "http://source.example.test" },
		func(in *attachmentChunkInput) { in.Peer = h.config.Origin },
		func(in *attachmentChunkInput) { in.Data = make([]byte, attachmentChunkBytes+1) },
	} {
		in := valid
		change(&in)
		if _, err := h.attachmentChunk(context.Background(), "merge-file-write", in); err == nil {
			t.Fatal("unsafe chunk accepted")
		}
	}
}

func TestAttachmentCacheReclaimsOnlyExpiredOwnedFiles(t *testing.T) {
	h, _ := fixture(t)
	root, err := h.chunkRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	old := strings.Repeat("a", 64) + ".part"
	newer := strings.Repeat("b", 64) + ".blob"
	for _, name := range []string{old, newer, "keep.txt"} {
		if err := root.WriteFile(name, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for _, name := range []string{old, "keep.txt"} {
		if err := os.Chtimes(filepath.Join(h.config.Root, "attachment-chunks-v1", name), now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	used, err := chunkCacheUsage(root, now)
	if err != nil || used != 4 {
		t.Fatalf("cache usage: %d %v", used, err)
	}
	if _, err := root.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired partial retained")
	}
	if _, err := root.Stat("keep.txt"); err != nil {
		t.Fatal("unowned file removed")
	}
}

// Explicit isolated URLs only; never fall back to the live checkout database.
func TestChunkedAttachmentsTwoDatabases(t *testing.T) {
	aURL, bURL := os.Getenv("MULTICA_MERGE_TEST_SOURCE_URL"), os.Getenv("MULTICA_MERGE_TEST_PEER_URL")
	if aURL == "" || bURL == "" {
		t.Skip("two isolated managed merge databases required")
	}
	if aURL == bURL {
		t.Fatal("separate databases required")
	}
	ctx := context.Background()
	email := uuid.NewString() + "@example.test"
	type center struct {
		h       *Handler
		fx      *testutil.Fixture
		uploads string
	}
	setup := func(address, origin string) center {
		pool, err := pgxpool.New(ctx, address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		fx := testutil.New(pool, "", "")
		fx.UserID = fx.User(t, "chunk owner", email)
		h, err := New(pool, Config{Owner: fx.UserID, Origin: origin, Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return center{h, fx, t.TempDir()}
	}
	a, b := setup(aURL, "https://a.example.test"), setup(bURL, "https://b.example.test")
	a.fx.WorkspaceID = a.fx.Workspace(t, "chunk workspace", "chunk-"+uuid.NewString())
	workspace := a.fx.WorkspaceID
	a.fx.Member(t, workspace, a.fx.UserID, "owner")
	issue := a.fx.Issue(t, "chunk fixture")
	for _, table := range contentTables {
		b.fx.Cleanup(t, "DELETE FROM "+quoted(table.name)+" t WHERE "+table.scope, workspace)
	}
	b.fx.Cleanup(t, `DELETE FROM member WHERE workspace_id=$1`, workspace)
	b.fx.Cleanup(t, `DELETE FROM center_content_merge WHERE workspace_id=$1`, workspace)
	use := func(c center) {
		t.Setenv("LOCAL_UPLOAD_DIR", c.uploads)
		t.Setenv("LOCAL_UPLOAD_BASE_URL", c.h.config.Origin)
		t.Setenv("S3_BUCKET", "")
	}
	for i := 0; i < 3; i++ {
		key := uuid.NewString()
		data := bytes.Repeat([]byte{byte(i + 1)}, 6<<20)
		if err := os.WriteFile(filepath.Join(a.uploads, key), data, 0600); err != nil {
			t.Fatal(err)
		}
		a.fx.Insert(t, "attachment", testutil.Cols{"workspace_id": workspace, "issue_id": issue, "uploader_type": "member", "uploader_id": a.fx.UserID, "filename": key, "url": "/uploads/" + key, "content_type": "application/octet-stream", "size_bytes": len(data)})
	}
	use(a)
	if _, err := a.h.exportContent(ctx, mergeInput{Workspace: workspace, Peer: b.h.config.Origin}); !errors.Is(err, ws.ErrLimit) {
		t.Fatal("legacy 16 MiB guard changed")
	}
	bundle, err := a.h.exportContent(ctx, mergeInput{Workspace: workspace, Peer: b.h.config.Origin, AttachmentMode: "chunked"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 0 || bundle.AttachmentMode != "chunked" {
		t.Fatal("file bytes leaked into manifest")
	}
	encoded, _ := json.Marshal(bundle)
	if len(encoded) > 1<<20 {
		t.Fatal("manifest unexpectedly includes large data")
	}
	use(b)
	input := mergeInput{Workspace: workspace, Peer: a.h.config.Origin, Bundle: &bundle}
	if _, err := b.h.mergeContent(ctx, input); err == nil {
		t.Fatal("published metadata before staging files")
	}
	var count int
	b.fx.QueryRow(t, `SELECT count(*) FROM workspace WHERE id=$1`, workspace).Scan(&count)
	if count != 0 {
		t.Fatal("incomplete transfer partially published workspace")
	}
	chunks := 0
	for _, row := range bundle.Records {
		if row.Table != "attachment" {
			continue
		}
		in := attachmentChunkInput{Workspace: workspace, Peer: a.h.config.Origin, Attachment: textValue(row.Fields["id"]), Hash: textValue(row.Fields["content_sha256"]), Size: 6 << 20}
		use(b)
		state, err := b.h.attachmentChunk(ctx, "merge-file-status", in)
		if err != nil || state.Offset != 0 || state.Ready {
			t.Fatalf("initial status: %+v %v", state, err)
		}
		for !state.Ready {
			read := in
			read.Peer = b.h.config.Origin
			read.Offset = state.Offset
			use(a)
			part, err := a.h.attachmentChunk(ctx, "merge-file-read", read)
			if err != nil || len(part.Data) > attachmentChunkBytes {
				t.Fatalf("read: %v", err)
			}
			write := in
			write.Offset = state.Offset
			write.Data = part.Data
			use(b)
			state, err = b.h.attachmentChunk(ctx, "merge-file-write", write)
			if err != nil {
				t.Fatal(err)
			}
			chunks++
			if write.Offset == 0 {
				duplicate, err := b.h.attachmentChunk(ctx, "merge-file-write", write)
				if err != nil || duplicate.Offset != state.Offset {
					t.Fatal("duplicate chunk changed offset")
				}
				corrupt := write
				corrupt.Data = append([]byte{}, write.Data...)
				corrupt.Data[0] ^= 1
				if _, err := b.h.attachmentChunk(ctx, "merge-file-write", corrupt); !errors.Is(err, ws.ErrScope) {
					t.Fatal("conflicting retry accepted")
				}
				restarted, err := New(b.h.pool, b.h.config)
				if err != nil {
					t.Fatal(err)
				}
				b.h = restarted
				resume, err := b.h.attachmentChunk(ctx, "merge-file-status", in)
				if err != nil || resume.Offset != attachmentChunkBytes {
					t.Fatal("restart lost confirmed bytes")
				}
			}
		}
		if _, err := b.h.stagedAttachment(workspace, "https://different-peer.example.test", row); err == nil {
			t.Fatal("staged file escaped peer binding")
		}
	}
	if chunks != 18 {
		t.Fatalf("wanted 18 bounded chunks, got %d", chunks)
	}
	t.Run("integrity and empty files", func(t *testing.T) {
		use(b)
		in := attachmentChunkInput{Workspace: workspace, Peer: a.h.config.Origin, Attachment: uuid.NewString(), Hash: contentHash([]byte("good")), Size: 4, Data: []byte("evil")}
		if _, err := b.h.attachmentChunk(ctx, "merge-file-write", in); !errors.Is(err, ws.ErrScope) {
			t.Fatalf("bad whole-file hash accepted: %v", err)
		}
		in.Data = nil
		if state, err := b.h.attachmentChunk(ctx, "merge-file-status", in); err != nil || state.Offset != 0 || state.Ready {
			t.Fatalf("corrupt partial survived: %+v %v", state, err)
		}
		in.Attachment, in.Hash, in.Size = uuid.NewString(), contentHash(nil), 0
		if state, err := b.h.attachmentChunk(ctx, "merge-file-write", in); err != nil || !state.Ready || state.Offset != 0 {
			t.Fatalf("empty file failed: %+v %v", state, err)
		}
	})
	t.Run("workspace scope", func(t *testing.T) {
		use(a)
		otherConfig := a.h.config
		otherConfig.Owner = uuid.NewString()
		other, err := New(a.h.pool, otherConfig)
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"merge-file-status", "merge-file-read", "merge-file-write"} {
			in := attachmentChunkInput{Workspace: workspace, Peer: b.h.config.Origin, Attachment: uuid.NewString(), Hash: contentHash(nil)}
			if _, err := other.attachmentChunk(ctx, action, in); err == nil {
				t.Fatalf("non-owner workspace access allowed: %s", action)
			}
		}
	})
	use(b)
	result, err := b.h.mergeContent(ctx, input)
	if err != nil || len(result.Conflicts) != 0 {
		t.Fatalf("apply: %+v %v", result, err)
	}
	b.fx.QueryRow(t, `SELECT count(*) FROM attachment WHERE workspace_id=$1`, workspace).Scan(&count)
	if count != 3 {
		t.Fatal("attachment rows missing")
	}
	copy, err := b.h.exportContent(ctx, mergeInput{Workspace: workspace, Peer: a.h.config.Origin, AttachmentMode: "chunked"})
	if err != nil || len(copy.Files) != 0 {
		t.Fatalf("destination manifest: %v", err)
	}
	for _, row := range copy.Records {
		if row.Table != "attachment" {
			continue
		}
		var url string
		b.fx.QueryRow(t, `SELECT url FROM attachment WHERE id=$1`, textValue(row.Fields["id"])).Scan(&url)
		data, err := readAttachment(url)
		if err != nil || contentHash(data) != textValue(row.Fields["content_sha256"]) {
			t.Fatal("published bytes do not match digest")
		}
	}
	if again, err := b.h.mergeContent(ctx, input); err != nil || again.Updated != 0 {
		t.Fatalf("retry not idempotent: %+v %v", again, err)
	}
	t.Run("reverse exchange reuses matching local bytes", func(t *testing.T) {
		use(a)
		for _, row := range copy.Records {
			if row.Table != "attachment" {
				continue
			}
			in := attachmentChunkInput{Workspace: workspace, Peer: b.h.config.Origin, Attachment: textValue(row.Fields["id"]), Hash: textValue(row.Fields["content_sha256"]), Size: 6 << 20}
			state, err := a.h.attachmentChunk(ctx, "merge-file-status", in)
			if err != nil || !state.Ready || state.Offset != in.Size {
				t.Fatalf("matching local file not reused: %+v %v", state, err)
			}
		}
		if result, err := a.h.mergeContent(ctx, mergeInput{Workspace: workspace, Peer: b.h.config.Origin, Bundle: &copy}); err != nil || result.Updated != 0 || len(result.Conflicts) != 0 {
			t.Fatalf("reverse exchange changed equal records: %+v %v", result, err)
		}
	})
	t.Run("file disappears after manifest", func(t *testing.T) {
		use(a)
		for _, row := range bundle.Records {
			if row.Table != "attachment" {
				continue
			}
			var key string
			a.fx.QueryRow(t, `SELECT filename FROM attachment WHERE id=$1`, textValue(row.Fields["id"])).Scan(&key)
			if err := os.Rename(filepath.Join(a.uploads, key), filepath.Join(a.uploads, key+".moved")); err != nil {
				t.Fatal(err)
			}
			in := attachmentChunkInput{Workspace: workspace, Peer: b.h.config.Origin, Attachment: textValue(row.Fields["id"]), Hash: textValue(row.Fields["content_sha256"]), Size: 6 << 20}
			state, err := a.h.attachmentChunk(ctx, "merge-file-read", in)
			if err != nil || !state.Unavailable || len(state.Data) != 0 || state.Ready {
				t.Fatalf("missing file did not produce warning: %+v %v", state, err)
			}
			break
		}
	})
}
