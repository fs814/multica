package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestIssueTableTimestampCursorFormats(t *testing.T) {
	for _, value := range []string{
		"2026-09-24T06:00:00.123456Z",
		"2026-09-24T06:00:00.123456+05:30",
		"2026-09-24 06:00:00+00",
		"2026-09-24 06:00:00.123456-07",
		"2026-09-24 06:00:00.123456+05:30",
		"1900-01-01 06:00:00.123456-00:43:08",
		"invalid", "2026-09-24 06:00:00", "2026-09-24 06:00:00+99",
	} {
		t.Run(value, func(t *testing.T) {
			cursor := issueTableCursor{SortValue: &value, RowID: "00000000-0000-4000-8000-000000000001"}
			w := httptest.NewRecorder()
			_, ok := (resolvedIssueTableSort{expression: "i.last_activity_at", direction: "desc", castType: "timestamptz", idOnlyTie: true}).cursorPredicate(w, &cursor, func(any) string { return "$1" })
			want := value != "invalid" && value != "2026-09-24 06:00:00" && value != "2026-09-24 06:00:00+99"
			if ok != want {
				t.Fatalf("accepted=%v, want %v: %s", ok, want, w.Body.String())
			}
			if !ok && w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
}

type issueTableTimezoneStarter struct {
	txStarter
	zone string
}

func (s issueTableTimezoneStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// SET does not establish a snapshot before the handler sets isolation level.
	if _, err = tx.Exec(ctx, "SET LOCAL TIME ZONE '"+strings.ReplaceAll(s.zone, "'", "''")+"'"); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func TestIssueTableTimestampContinuousPagination(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	project := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "timestamp pagination"})
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), "DELETE FROM issue WHERE project_id=$1", project) })
	dbfx.Exec(t, `
        WITH reserved AS (
            UPDATE workspace SET issue_counter = GREATEST(issue_counter,
                (SELECT COALESCE(MAX(number),0) FROM issue WHERE workspace_id=$1)) + 1001
            WHERE id=$1 RETURNING issue_counter
        )
        INSERT INTO issue(workspace_id,project_id,title,status,creator_type,creator_id,number,created_at,updated_at,last_activity_at)
        SELECT $1,$2,'timestamp-' || n,'todo','member',$3,reserved.issue_counter - 1001 + n,
            '1900-01-01T00:00:00.123456Z'::timestamptz + (n % 17) * interval '1 second',
            '1900-01-01T00:00:00.654321Z'::timestamptz + (n % 23) * interval '1 second',
            '1900-01-01T00:00:00.111111Z'::timestamptz + (n % 31) * interval '1 second'
        FROM generate_series(1,1001) n CROSS JOIN reserved`, testWorkspaceID, project, testUserID)
	for _, zone := range []string{"UTC", "Asia/Kolkata", "America/Los_Angeles", "Africa/Monrovia"} {
		for _, field := range []string{"created_at", "updated_at", "last_activity"} {
			for _, direction := range []string{"asc", "desc"} {
				t.Run(zone+"/"+field+"/"+direction, func(t *testing.T) {
					h := *testHandler
					h.TxStarter = issueTableTimezoneStarter{txStarter: h.TxStarter, zone: zone}
					column, tie := field, ", created_at DESC, id DESC"
					if field == "last_activity" {
						column, tie = "last_activity_at", ", id DESC"
					}
					expected, err := testPool.Query(context.Background(), "SELECT id::text FROM issue WHERE project_id=$1 ORDER BY "+column+" "+direction+tie, project)
					if err != nil {
						t.Fatal(err)
					}
					ids := []string{}
					for expected.Next() {
						var id string
						if err := expected.Scan(&id); err != nil {
							t.Fatal(err)
						}
						ids = append(ids, id)
					}
					if err := expected.Err(); err != nil {
						t.Fatal(err)
					}
					expected.Close()
					if len(ids) != 1001 {
						t.Fatalf("expected fixture count=%d", len(ids))
					}
					groupKey := "status:todo"
					var cursor *string
					seen := 0
					for page := 0; ; page++ {
						if page > 11 {
							t.Fatal("pagination did not terminate")
						}
						req := issueTableRowsRequest{
							Query: issueTableQuerySpec{Scope: issueTableScope{Kind: "project", ProjectID: project}, Sort: issueTableSortRequest{Field: field, Direction: direction}},
							Group: issueTableGroupSpec{Kind: "status"}, GroupKey: &groupKey,
							Page: issueTablePageRequest{Limit: 100, Cursor: cursor},
						}
						var response issueTableRowsResponse
						testutil.Call(t, h.ListIssueTableRows, newRequest("POST", "/api/issues/table/rows", req)).Want(http.StatusOK).JSON(&response)
						for _, row := range response.Rows {
							if seen >= len(ids) || row.Issue.ID != ids[seen] {
								t.Fatalf("row %d is duplicated, missing, or out of order: %s", seen, row.Issue.ID)
							}
							seen++
						}
						if response.NextCursor == nil {
							break
						}
						if len(response.Rows) != 100 {
							t.Fatalf("nonterminal page length=%d", len(response.Rows))
						}
						cursor = response.NextCursor
						_, decoded, ok := normalizeIssueTablePage(httptest.NewRecorder(), issueTablePageRequest{Cursor: cursor})
						if !ok || decoded.SortValue == nil {
							t.Fatal("generated cursor cannot be decoded")
						}
						if _, err := time.Parse(time.RFC3339Nano, *decoded.SortValue); err != nil || !strings.HasSuffix(*decoded.SortValue, "Z") {
							t.Fatalf("generated cursor is not UTC RFC3339: %s", *decoded.SortValue)
						}
						if page%2 == 0 {
							// Alternate new cursors with database text emitted by older servers.
							tx, err := h.TxStarter.Begin(context.Background())
							if err != nil {
								t.Fatal(err)
							}
							var legacy string
							err = tx.QueryRow(context.Background(), "SELECT "+column+"::text FROM issue WHERE id=$1", decoded.RowID).Scan(&legacy)
							_ = tx.Rollback(context.Background())
							if err != nil {
								t.Fatal(err)
							}
							decoded.SortValue = &legacy
							cursor = encodeIssueTableCursor(*decoded)
						}
					}
					if seen != len(ids) {
						t.Fatalf("received %d of %d rows", seen, len(ids))
					}
				})
			}
		}
	}
}
