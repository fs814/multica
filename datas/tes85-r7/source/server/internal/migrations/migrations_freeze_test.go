package migrations

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoricalMigrationPrefixFreeze(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")
	check := func(t *testing.T, changed []string, wantProblem bool) {
		t.Helper()
		problems := migrationPrefixProblems(changed)
		if (len(problems) != 0) != wantProblem {
			t.Fatalf("migration prefix problems = %v, want rejection %v", problems, wantProblem)
		}
	}
	t.Run("unchanged", func(t *testing.T) { check(t, files, false) })
	t.Run("file order is irrelevant", func(t *testing.T) {
		reversed := append([]string(nil), files...)
		for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
			reversed[i], reversed[j] = reversed[j], reversed[i]
		}
		check(t, reversed, false)
	})
	count := 0
	for number, stems := range historicalWorkflowMigrationCollisions {
		count += len(stems)
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			t.Run("delete entire group", func(t *testing.T) {
				var changed []string
				for _, file := range files {
					if !strings.HasPrefix(filepath.Base(file), fmt.Sprintf("%d_", number)) {
						changed = append(changed, file)
					}
				}
				check(t, changed, true)
			})
			t.Run("append", func(t *testing.T) {
				check(t, append(append([]string(nil), files...), fmt.Sprintf("%d_unapproved.up.sql", number)), true)
			})
			for _, stem := range stems {
				t.Run(stem, func(t *testing.T) {
					var removed []string
					replaced := append([]string(nil), files...)
					for i, file := range files {
						if filepath.Base(file) == stem+".up.sql" {
							replaced[i] = stem + "_replacement.up.sql"
						} else {
							removed = append(removed, file)
						}
					}
					t.Run("delete", func(t *testing.T) { check(t, removed, true) })
					t.Run("replace", func(t *testing.T) { check(t, replaced, true) })
				})
			}
		})
	}
	if len(historicalWorkflowMigrationCollisions) != 25 || count != 63 {
		t.Fatalf("frozen R6 set changed: %d groups, %d stems", len(historicalWorkflowMigrationCollisions), count)
	}
	t.Run("new unique number", func(t *testing.T) {
		check(t, append(append([]string(nil), files...), "999_new.up.sql"), false)
	})
	for _, prefix := range []string{"129", "256", "999"} {
		t.Run("reject new duplicate "+prefix, func(t *testing.T) {
			check(t, append(append([]string(nil), files...), prefix+"_new_a.up.sql", prefix+"_new_b.up.sql"), true)
		})
	}
	t.Run("numeric alias cannot bypass freeze", func(t *testing.T) {
		check(t, append(append([]string(nil), files...), "0232_alias.up.sql"), true)
	})
	t.Run("legacy boundary unchanged", func(t *testing.T) {
		check(t, append(append([]string(nil), files...), "128_legacy_a.up.sql", "128_legacy_b.up.sql"), false)
	})
}
