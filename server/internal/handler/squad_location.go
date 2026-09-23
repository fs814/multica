package handler

import (
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
)

// Only safe display fields enter the briefing. Never serialize runtime metadata,
// agent custom_env, or an inaccessible machine's name, identity or status.
func squadExecutionLocation(ag db.Agent, runtimes []db.AgentRuntime, workspaceID, viewerID pgtype.UUID) string {
	if !ag.RuntimeID.Valid {
		return "execution node: unbound"
	}
	for _, rt := range runtimes {
		if rt.ID != ag.RuntimeID || rt.WorkspaceID != workspaceID || (rt.Visibility != "public" && (!viewerID.Valid || rt.OwnerID != viewerID)) {
			continue
		}
		name := strings.TrimSpace(rt.CustomName.String)
		if name == "" {
			name = rt.Name
		}
		id := rt.DaemonID.String
		if id == "" {
			id = util.UUIDToString(rt.ID)
		}
		if len(id) > 8 {
			id = id[:8]
		}
		name = strings.Join(strings.Fields(name), " ")
		return "execution node (current binding): " + name + " [" + id + "] / " + rt.Provider + " / " + rt.Status
	}
	return "execution node: unknown or not visible"
}
