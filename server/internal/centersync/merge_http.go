package centersync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	ws "github.com/multica-ai/multica/server/internal/worksync"
)

func (h *Handler) serveMerge(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var input mergeInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, ws.MaxWireBytes))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		respondError(w, ws.ErrOperation)
		return
	}
	var result any
	var err error
	switch strings.TrimPrefix(r.URL.Path, Prefix+"/") {
	case "merge-export":
		result, err = h.exportContent(ctx, input)
	case "merge-apply":
		result, err = h.mergeContent(ctx, input)
	case "merge-list":
		if input.Bundle != nil || input.Workspace != "" || origin(input.Peer) != nil || input.Peer == h.config.Origin {
			respondError(w, ws.ErrScope)
			return
		}
		rows, e := h.pool.Query(ctx, `SELECT w.id::text FROM workspace w JOIN member m ON m.workspace_id=w.id WHERE m.user_id=$1 AND m.role='owner' ORDER BY w.id LIMIT 101`, h.config.Owner)
		if e != nil {
			respondError(w, e)
			return
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if len(ids) > 100 {
			err = ws.ErrLimit
		}
		result = map[string]any{"workspaces": ids}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		respondError(w, err)
		return
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > ws.MaxWireBytes {
		respondError(w, ws.ErrLimit)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
