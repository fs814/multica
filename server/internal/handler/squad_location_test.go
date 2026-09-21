package handler

import (
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"testing"
)

func TestSquadExecutionLocationPrivacy(t *testing.T) {
	ws := util.MustParseUUID("00000000-0000-4000-8000-000000000001")
	owner := util.MustParseUUID("00000000-0000-4000-8000-000000000002")
	other := util.MustParseUUID("00000000-0000-4000-8000-000000000003")
	rt := db.AgentRuntime{ID: other, WorkspaceID: ws, OwnerID: owner, Visibility: "private", Name: "secret-host", Provider: "codex", Status: "offline", CustomName: pgtype.Text{String: "Renamed", Valid: true}}
	ag := db.Agent{RuntimeID: rt.ID}
	for _, viewer := range []pgtype.UUID{other, {}} {
		got := squadExecutionLocation(ag, []db.AgentRuntime{rt}, ws, viewer)
		if got != "execution node: unknown or not visible" {
			t.Fatalf("leaked private location: %s", got)
		}
	}
	if got := squadExecutionLocation(ag, []db.AgentRuntime{rt}, ws, owner); !strings.Contains(got, "Renamed") || !strings.Contains(got, "offline") {
		t.Fatal(got)
	}
	rt.Visibility = "public"
	if got := squadExecutionLocation(ag, []db.AgentRuntime{rt}, other, owner); got != "execution node: unknown or not visible" {
		t.Fatal("cross-workspace leak", got)
	}
	if got := squadExecutionLocation(ag, []db.AgentRuntime{rt}, ws, other); !strings.Contains(got, "Renamed") {
		t.Fatal(got)
	}
	if got := squadExecutionLocation(db.Agent{}, nil, ws, owner); got != "execution node: unbound" {
		t.Fatal(got)
	}
}
