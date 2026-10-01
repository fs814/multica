package centersync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

// Database IDs differ when the same machine registered independently on both
// centers. Only the wire identity is canonical; never rewrite local primary
// keys, queues, sessions or daemon leases to make them match.
func runtimeIdentity(workspace string, r contentRecord) string {
	provider := "provider:" + textValue(r.Fields["provider"])
	if profile := textValue(r.Fields["profile_id"]); profile != "" {
		provider = "profile:" + profile
	}
	parts, _ := json.Marshal([]string{"multica-center-runtime", workspace, textValue(r.Fields["daemon_id"]), provider})
	return uuid.NewSHA1(uuid.NameSpaceURL, parts).String()
}

func isRuntimeIdentityField(table, field string) bool {
	switch table {
	case "runtime_profile":
		return field == "protocol_family" || field == "runtime_type"
	case "agent_runtime":
		return field == "daemon_id" || field == "provider" || field == "profile_id" || field == "owner_id" || field == "runtime_mode"
	default:
		return false
	}
}

func normalizeRuntimeIdentities(b *contentBundle) error {
	ids, seen := map[string]string{}, map[string]bool{}
	for _, r := range b.Records {
		if r.Table != "agent_runtime" {
			continue
		}
		id := runtimeIdentity(b.Workspace, r)
		if seen[id] {
			return ws.ErrScope
		}
		seen[id] = true
		ids[textValue(r.Fields["id"])] = id
		r.Fields["id"] = rawValue(id)
	}
	for _, r := range b.Records {
		if r.Table == "agent" {
			if id := ids[textValue(r.Fields["runtime_id"])]; id != "" {
				r.Fields["runtime_id"] = rawValue(id)
			} else {
				// Cloud runtimes have no portable machine identity.
				r.Fields["runtime_id"] = rawValue(nil)
			}
		}
	}
	return nil
}

func validateRuntimeRecord(workspace string, r contentRecord) error {
	if r.Table == "agent" {
		v := r.Fields["runtime_id"]
		if string(v) != "null" && !validID(textValue(v)) {
			return ws.ErrScope
		}
	}
	if r.Table != "agent_runtime" {
		return nil
	}
	for _, field := range []string{"daemon_id", "name", "runtime_mode", "provider"} {
		var value string
		if json.Unmarshal(r.Fields[field], &value) != nil || strings.TrimSpace(value) == "" || len(value) > 1024 {
			return ws.ErrScope
		}
	}
	for _, field := range []string{"owner_id", "profile_id"} {
		if string(r.Fields[field]) != "null" && !validID(textValue(r.Fields[field])) {
			return ws.ErrScope
		}
	}
	if textValue(r.Fields["runtime_mode"]) != "local" || textValue(r.Fields["id"]) != runtimeIdentity(workspace, r) {
		return ws.ErrScope
	}
	return nil
}

func resolveRuntimeIdentities(ctx context.Context, tx pgx.Tx, workspace string, records []contentRecord) (map[string]string, error) {
	result := map[string]string{}
	for _, r := range records {
		if r.Table != "agent_runtime" {
			continue
		}
		key := textValue(r.Fields["id"])
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM agent_runtime WHERE workspace_id=$1 AND daemon_id=$2 AND ((profile_id IS NULL AND $4::text='' AND provider=$3) OR profile_id::text=$4)`, workspace, textValue(r.Fields["daemon_id"]), textValue(r.Fields["provider"]), textValue(r.Fields["profile_id"])).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			id = key
		} else if err != nil {
			return nil, err
		}
		result[key] = id
	}
	return result, nil
}
