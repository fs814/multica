package centersync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

const attachmentChunkBytes = 1 << 20
const maxAttachmentBytes = 8 << 20
const attachmentCacheBytes = 1 << 30
const attachmentCacheLifetime = 24 * time.Hour

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var cacheEntryPattern = regexp.MustCompile(`^[a-f0-9]{64}\.(part|blob)$`)

func validDigest(value string) bool { return digestPattern.MatchString(value) }

type attachmentChunkInput struct {
	Workspace  string `json:"workspace"`
	Peer       string `json:"peer"`
	Attachment string `json:"attachment"`
	Hash       string `json:"hash"`
	Size       int64  `json:"size"`
	Offset     int64  `json:"offset"`
	Data       []byte `json:"data,omitempty"`
}
type attachmentChunkResult struct {
	Attachment  string `json:"attachment"`
	Hash        string `json:"hash"`
	Size        int64  `json:"size"`
	Offset      int64  `json:"offset"`
	Ready       bool   `json:"ready"`
	Unavailable bool   `json:"unavailable,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

func (h *Handler) chunkKey(in attachmentChunkInput) string {
	return contentHash([]byte(strings.Join([]string{h.config.Owner, in.Workspace, in.Peer, in.Attachment, in.Hash, strconv.FormatInt(in.Size, 10)}, "\n")))
}

// This directory is private, separate from publicly served uploads. Names are
// derived only from validated scope fields; no caller supplies a filesystem path.
func (h *Handler) chunkRoot() (*os.Root, error) {
	dir := filepath.Join(h.config.Root, "attachment-chunks-v1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, ws.ErrDenied
	}
	return os.OpenRoot(dir)
}

// Called under the database cache lock. Reclaim only this feature's regular
// staging files, only on an explicit request, never via a background worker.
func chunkCacheUsage(root *os.Root, now time.Time) (int64, error) {
	dir, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	var total int64
	for {
		entries, err := dir.ReadDir(256)
		if err != nil && err != io.EOF {
			return 0, err
		}
		for _, entry := range entries {
			if !cacheEntryPattern.MatchString(entry.Name()) {
				continue
			}
			info, e := entry.Info()
			if e != nil {
				return 0, e
			}
			if !info.Mode().IsRegular() {
				return 0, ws.ErrScope
			}
			if now.Sub(info.ModTime()) > attachmentCacheLifetime {
				if e = root.Remove(entry.Name()); e != nil {
					return 0, e
				}
				continue
			}
			total += info.Size()
		}
		if err == io.EOF {
			return total, nil
		}
	}
}

func cachedBytes(root *os.Root, name string, size int64, hash string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != size || size > maxAttachmentBytes {
		return nil, ws.ErrScope
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size || contentHash(data) != hash {
		return nil, ws.ErrScope
	}
	return data, nil
}

func cacheComplete(root *os.Root, key string, data []byte) error {
	temporary := contentHash([]byte(uuid.NewString())) + ".part"
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return root.Link(temporary, key+".blob")
}

func (h *Handler) stagedAttachment(workspace, peer string, r contentRecord) (contentFile, error) {
	in := attachmentChunkInput{Workspace: workspace, Peer: peer, Attachment: textValue(r.Fields["id"]), Hash: textValue(r.Fields["content_sha256"])}
	if json.Unmarshal(r.Fields["size_bytes"], &in.Size) != nil {
		return contentFile{}, ws.ErrOperation
	}
	root, err := h.chunkRoot()
	if err != nil {
		return contentFile{}, err
	}
	defer root.Close()
	data, err := cachedBytes(root, h.chunkKey(in)+".blob", in.Size, in.Hash)
	if errors.Is(err, os.ErrNotExist) {
		return contentFile{}, ws.ErrScope
	}
	return contentFile{in.Attachment, data}, err
}

func (h *Handler) serveAttachmentChunk(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var in attachmentChunkInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		respondError(w, ws.ErrOperation)
		return
	}
	out, err := h.attachmentChunk(ctx, strings.TrimPrefix(r.URL.Path, Prefix+"/"), in)
	if err != nil {
		respondError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handler) attachmentChunk(ctx context.Context, action string, in attachmentChunkInput) (attachmentChunkResult, error) {
	out := attachmentChunkResult{Attachment: in.Attachment, Hash: in.Hash, Size: in.Size}
	if !validID(in.Workspace) || !validID(in.Attachment) || !validDigest(in.Hash) || origin(in.Peer) != nil || in.Peer == h.config.Origin || in.Size < 0 || in.Size > maxAttachmentBytes || in.Offset < 0 || in.Offset > in.Size || len(in.Data) > attachmentChunkBytes {
		return out, ws.ErrOperation
	}
	if action != "merge-file-read" && action != "merge-file-status" && action != "merge-file-write" {
		return out, ws.ErrOperation
	}
	if action != "merge-file-write" && len(in.Data) != 0 {
		return out, ws.ErrOperation
	}
	if action == "merge-file-status" && in.Offset != 0 {
		return out, ws.ErrOperation
	}
	if action == "merge-file-write" && (in.Offset+int64(len(in.Data)) > in.Size || len(in.Data) == 0 && in.Size != 0) {
		return out, ws.ErrOperation
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	// Serialize scoped staging updates, including duplicate requests and cache
	// quota checks across API processes. Ownership is checked on every request.
	if action != "merge-file-read" {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "center-attachment-cache:"+h.config.Root); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE`, in.Workspace); err != nil {
		return out, err
	}
	if err = h.owns(ctx, tx, in.Workspace, action != "merge-file-read"); err != nil {
		return out, err
	}
	if action == "merge-file-read" {
		data, err := h.attachmentBytes(ctx, tx, in)
		if errors.Is(err, errAttachmentUnavailable) {
			out.Unavailable = true
			return out, nil
		}
		if err != nil {
			return out, err
		}
		end := min(in.Offset+attachmentChunkBytes, in.Size)
		out.Data, out.Offset, out.Ready = data[in.Offset:end], end, end == in.Size
		return out, nil
	}
	root, err := h.chunkRoot()
	if err != nil {
		return out, err
	}
	defer root.Close()
	used, err := chunkCacheUsage(root, time.Now())
	if err != nil {
		return out, err
	}
	key := h.chunkKey(in)
	if _, err = cachedBytes(root, key+".blob", in.Size, in.Hash); err == nil {
		if err = root.Chtimes(key+".blob", time.Now(), time.Now()); err != nil {
			return out, err
		}
		out.Offset, out.Ready = in.Size, true
		return out, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	// Reuse an existing matching local attachment without sending its bytes
	// across the network again. A different local copy is never replaced here.
	if action == "merge-file-status" {
		data, e := h.attachmentBytes(ctx, tx, in)
		if e == nil {
			if used+int64(len(data)) > attachmentCacheBytes {
				return out, ws.ErrLimit
			}
			if e = cacheComplete(root, key, data); e != nil {
				return out, e
			}
			out.Offset, out.Ready = in.Size, true
			return out, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) && !errors.Is(e, errAttachmentUnavailable) && !errors.Is(e, ws.ErrScope) {
			return out, e
		}
	}
	flags := os.O_RDONLY
	if action == "merge-file-write" {
		flags = os.O_RDWR | os.O_CREATE
	}
	f, err := root.OpenFile(key+".part", flags, 0600)
	if errors.Is(err, os.ErrNotExist) && action == "merge-file-status" {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() || info.Size() > in.Size {
		return out, ws.ErrScope
	}
	out.Offset = info.Size()
	if err = root.Chtimes(key+".part", time.Now(), time.Now()); err != nil {
		return out, err
	}
	if action == "merge-file-write" {
		if in.Offset > info.Size() {
			return out, ws.ErrCursor
		}
		if in.Offset < info.Size() {
			if in.Offset+int64(len(in.Data)) > info.Size() {
				return out, ws.ErrCursor
			}
			old := make([]byte, len(in.Data))
			if _, err = f.ReadAt(old, in.Offset); err != nil {
				return out, err
			}
			if !bytes.Equal(old, in.Data) {
				return out, ws.ErrScope
			}
		} else {
			if used+int64(len(in.Data)) > attachmentCacheBytes {
				return out, ws.ErrLimit
			}
			if _, err = f.WriteAt(in.Data, in.Offset); err != nil {
				return out, err
			}
			if err = f.Sync(); err != nil {
				return out, err
			}
			out.Offset += int64(len(in.Data))
		}
	}
	if out.Offset == in.Size {
		if _, err = cachedBytes(root, key+".part", in.Size, in.Hash); err != nil {
			_ = f.Close()
			_ = root.Remove(key + ".part")
			return out, err
		}
		if err = f.Close(); err != nil {
			return out, err
		}
		if err = root.Rename(key+".part", key+".blob"); err != nil {
			return out, err
		}
		out.Ready = true
	}
	return out, nil
}

func (h *Handler) attachmentBytes(ctx context.Context, tx pgx.Tx, in attachmentChunkInput) ([]byte, error) {
	t, _ := tableFor("attachment")
	var address string
	if err := tx.QueryRow(ctx, `SELECT t.url FROM attachment t WHERE `+t.scope+` AND t.id=$2`, in.Workspace, in.Attachment).Scan(&address); err != nil {
		return nil, err
	}
	data, err := readAttachment(address)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != in.Size || contentHash(data) != in.Hash {
		return nil, ws.ErrScope
	}
	return data, nil
}
