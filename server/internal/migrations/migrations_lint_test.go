package migrations

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Historical artifacts are pinned by name and SHA-256 in frozen-migrations.json.
// This is an approved generation boundary, not an extensible lint allow-list.
const maxLegacyImplicitIndexPrefix = 466

var migrationPrefixPattern = regexp.MustCompile(`^(\d+)_`)

// PL/pgSQL unique_violation is an exception name, not a UNIQUE constraint.
var uniqueKeywordPattern = regexp.MustCompile(`\bUNIQUE\b`)

func TestMigrationFilesHaveMatchingDirections(t *testing.T) {
	files := migrationFilesForLint(t, "*.sql")

	directionsByStem := make(map[string]map[string]bool)
	for _, file := range files {
		stem, direction, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		if directionsByStem[stem] == nil {
			directionsByStem[stem] = make(map[string]bool)
		}
		directionsByStem[stem][direction] = true
	}

	for stem, directions := range directionsByStem {
		if !directions["up"] || !directions["down"] {
			t.Errorf("migration %s must have both .up.sql and .down.sql files", stem)
		}
	}
}

func TestMigrationNumericPrefixesStayUniqueAfterLegacySet(t *testing.T) {
	for _, problem := range frozenMigrationProblems(loadFrozenMigrations(t), migrationContents(t)) {
		t.Error(problem)
	}
}

func TestNewMigrationsDoNotCreateImplicitIndexes(t *testing.T) {
	manifest := loadFrozenMigrations(t)
	for _, file := range migrationFilesForLint(t, "*.up.sql") {
		stem := strings.TrimSuffix(filepath.Base(file), ".up.sql")
		match := migrationPrefixPattern.FindStringSubmatch(stem)
		if match == nil {
			continue
		}
		prefix, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("parse migration prefix for %s: %v", file, err)
		}
		if prefix <= maxLegacyImplicitIndexPrefix || filepath.Base(file) == manifest.AcceptedImplicitIndex {
			continue
		}

		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read migration %s: %v", file, err)
		}
		for lineNumber, line := range strings.Split(string(raw), "\n") {
			sql := strings.ToUpper(strings.TrimSpace(strings.SplitN(line, "--", 2)[0]))
			if sql == "" {
				continue
			}
			if strings.Contains(sql, "PRIMARY KEY") && !strings.Contains(sql, "PRIMARY KEY USING INDEX") {
				t.Errorf("%s:%d creates an implicit primary-key index; create a unique index concurrently in its own migration, then attach it with PRIMARY KEY USING INDEX", filepath.Base(file), lineNumber+1)
			}
			if uniqueKeywordPattern.MatchString(sql) && !strings.Contains(sql, "CREATE UNIQUE INDEX CONCURRENTLY") {
				t.Errorf("%s:%d creates an implicit unique index; use CREATE UNIQUE INDEX CONCURRENTLY in its own migration", filepath.Base(file), lineNumber+1)
			}
		}
	}
}

func migrationFilesForLint(t *testing.T, pattern string) []string {
	t.Helper()

	dir := realMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migration files matched %s in %s", pattern, dir)
	}
	sort.Strings(files)
	return files
}

func realMigrationsDir(t *testing.T) string {
	t.Helper()

	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration lint test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "migrations"))
}

func splitMigrationFilename(name string) (stem, direction string, ok bool) {
	for _, candidateDirection := range []string{"up", "down"} {
		suffix := fmt.Sprintf(".%s.sql", candidateDirection)
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), candidateDirection, true
		}
	}
	return "", "", false
}

func TestUniqueKeywordPatternDistinguishesExceptionNames(t *testing.T) {
	for _, tt := range []struct {
		sql  string
		want bool
	}{
		{"EXCEPTION WHEN unique_violation THEN", false},
		{"id uuid UNIQUE", true},
		{"CONSTRAINT delivery_key UNIQUE(comment_id, agent_id)", true},
		{"CREATE UNIQUE INDEX delivery_idx ON delivery(id)", true},
	} {
		t.Run(tt.sql, func(t *testing.T) {
			if got := uniqueKeywordPattern.MatchString(strings.ToUpper(tt.sql)); got != tt.want {
				t.Fatalf("UNIQUE keyword in %q = %v, want %v", tt.sql, got, tt.want)
			}
		})
	}
}
