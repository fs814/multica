package centersync

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestResolveOwner(t *testing.T) {
	id, other := uuid.NewString(), uuid.NewString()
	for _, tc := range []struct {
		name, id, email string
		matches         []string
		lookupErr       error
		want            string
		fail            bool
	}{
		{name: "legacy ID", id: id, want: id},
		{name: "email only", email: "owner@example.test", matches: []string{id}, want: id},
		{name: "matching ID", id: id, email: "owner@example.test", matches: []string{id}, want: id},
		{name: "different server ID", id: other, email: "owner@example.test", matches: []string{id}, fail: true},
		{name: "missing account", email: "owner@example.test", fail: true},
		{name: "ambiguous", email: "owner@example.test", matches: []string{id, other}, fail: true},
		{name: "invalid ID", email: "owner@example.test", matches: []string{"invalid"}, fail: true},
		{name: "invalid configured ID", id: "invalid", email: "owner@example.test", fail: true},
		{name: "display name", email: "Owner <owner@example.test>", fail: true},
		{name: "invalid email", email: "invalid", fail: true},
		{name: "database error", email: "owner@example.test", lookupErr: errors.New("private database credentials"), fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveOwner(tc.id, tc.email, func(email string) ([]string, error) {
				if email == "" || email != tc.email {
					t.Fatal("unexpected lookup")
				}
				return tc.matches, tc.lookupErr
			})
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
			if err != nil && strings.Contains(err.Error(), "credentials") {
				t.Fatal("leaked database error")
			}
		})
	}
}

func TestEmailEnvironmentFailsClosedWithoutDatabase(t *testing.T) {
	t.Setenv("MULTICA_CENTER_SYNC_OWNER_EMAIL", "owner@example.test")
	t.Setenv("MULTICA_CENTER_SYNC_OWNER_ID", "")
	t.Setenv("MULTICA_CENTER_SYNC_ENABLED", "0")
	if h, err := NewFromEnvironment(nil); h != nil || err != nil {
		t.Fatal("disabled sync must not query database")
	}
	t.Setenv("MULTICA_CENTER_SYNC_ENABLED", "1")
	if h, err := NewFromEnvironment(nil); h != nil || err == nil {
		t.Fatal("email-selected sync must reject an unavailable database")
	}
}
