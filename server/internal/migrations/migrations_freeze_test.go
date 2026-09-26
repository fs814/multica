package migrations

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type frozenManifest struct {
	SourceCommit          string              `json:"source_commit"`
	MaxPrefix             int                 `json:"max_prefix"`
	Files                 map[string]string   `json:"files"`
	CollisionChanges      map[string][]string `json:"accepted_collision_changes"`
	AcceptedImplicitIndex string              `json:"accepted_implicit_index"`
}

func loadFrozenMigrations(t *testing.T) frozenManifest {
	t.Helper()
	raw, err := os.ReadFile("testdata/frozen-migrations.json")
	if err != nil {
		t.Fatal(err)
	}
	// Changing the manifest requires an explicit policy review. Never regenerate
	// it from the working tree to make a failing migration pass.
	const digest = "bb7ee58440d7f38f7777d1a3e5e9d745a4794255fd876598a9afa0dfde364bf0"
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != digest {
		t.Fatal("frozen manifest changed; requires migration policy review")
	}
	var m frozenManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.SourceCommit != "e641ee11836fff0ee479d855237c259e059ac3fc" || len(m.Files) != 1334 || len(m.CollisionChanges) != 53 || m.MaxPrefix != 541 {
		t.Fatal("unexpected historical baseline")
	}
	return m
}

func migrationContents(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, f := range migrationFilesForLint(t, "*.sql") {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.Base(f)] = raw
	}
	return files
}

func frozenMigrationProblems(m frozenManifest, files map[string][]byte) []string {
	var problems []string
	for name, hash := range m.Files {
		raw, ok := files[name]
		if !ok {
			problems = append(problems, "missing frozen artifact: "+name)
		} else if fmt.Sprintf("%x", sha256.Sum256(raw)) != hash {
			problems = append(problems, "changed frozen artifact: "+name)
		}
	}
	newPrefixes := map[int]string{}
	for name, raw := range files {
		stem, direction, ok := splitMigrationFilename(name)
		match := migrationPrefixPattern.FindStringSubmatch(stem)
		if !ok || match == nil {
			problems = append(problems, "invalid migration filename: "+name)
			continue
		}
		if _, ok := m.Files[name]; ok {
			continue
		}
		n, err := strconv.Atoi(match[1])
		if err != nil || n <= m.MaxPrefix || match[1] != fmt.Sprintf("%03d", n) {
			problems = append(problems, "new migration in frozen range: "+name)
		}
		if _, ok := files[stem+".up.sql"]; !ok {
			problems = append(problems, "missing up: "+stem)
		}
		if _, ok := files[stem+".down.sql"]; !ok {
			problems = append(problems, "missing down: "+stem)
		}
		if direction == "up" {
			if other, ok := newPrefixes[n]; ok {
				problems = append(problems, "reused numeric prefix: "+other+", "+name)
			}
			newPrefixes[n] = name
		}
		for _, problem := range newIndexProblems(string(raw)) {
			problems = append(problems, name+": "+problem)
		}
	}
	sort.Strings(problems)
	return problems
}

// Collapse whitespace so multiline DDL cannot evade the index contract.
// Strip comments before checking; PL/pgSQL exception names remain whole tokens.
var sqlComments = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)
var primaryKeyDDL = regexp.MustCompile(`\bPRIMARY\s+KEY\b`)
var createIndexDDL = regexp.MustCompile(`\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+`)
var concurrentIndexDDL = regexp.MustCompile(`\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+`)

func newIndexProblems(raw string) []string {
	sql := strings.ToUpper(strings.Join(strings.Fields(sqlComments.ReplaceAllString(raw, " ")), " "))
	var problems []string
	withoutAttach := strings.ReplaceAll(sql, "PRIMARY KEY USING INDEX", "")
	if primaryKeyDDL.MatchString(withoutAttach) {
		problems = append(problems, "implicit primary key index")
	}
	withoutExplicit := strings.ReplaceAll(sql, "CREATE UNIQUE INDEX CONCURRENTLY", "CREATE INDEX CONCURRENTLY")
	withoutExplicit = strings.ReplaceAll(withoutExplicit, "UNIQUE USING INDEX", "USING INDEX")
	if uniqueKeywordPattern.MatchString(withoutExplicit) {
		problems = append(problems, "implicit unique index")
	}
	if len(createIndexDDL.FindAllStringIndex(sql, -1)) != len(concurrentIndexDDL.FindAllStringIndex(sql, -1)) {
		problems = append(problems, "index must be concurrent")
	}
	if concurrentIndexDDL.MatchString(sql) && (strings.Count(strings.TrimSuffix(sql, ";"), ";") != 0 || !strings.HasPrefix(sql, concurrentIndexDDL.FindString(sql))) {
		problems = append(problems, "concurrent index requires its own single-statement file")
	}
	return problems
}

func TestFrozenMigrationNegativeControls(t *testing.T) {
	m, files := loadFrozenMigrations(t), migrationContents(t)
	check := func(name string, change func(map[string][]byte), want bool) {
		t.Run(name, func(t *testing.T) {
			c := make(map[string][]byte, len(files))
			for k, v := range files {
				c[k] = v
			}
			change(c)
			p := frozenMigrationProblems(m, c)
			if (len(p) > 0) != want {
				t.Fatalf("problems=%v, want rejection=%v", p, want)
			}
		})
	}
	check("unchanged", func(map[string][]byte) {}, false)
	// Every member of all 53 debt groups, both directions, is protected.
	for _, stems := range m.CollisionChanges {
		for _, stem := range stems {
			for _, dir := range []string{"up", "down"} {
				name := stem + "." + dir + ".sql"
				check("edit/"+name, func(c map[string][]byte) { c[name] = append(append([]byte(nil), c[name]...), '\n') }, true)
				check("delete/"+name, func(c map[string][]byte) { delete(c, name) }, true)
				check("rename/"+name, func(c map[string][]byte) { c["999_renamed."+dir+".sql"] = c[name]; delete(c, name) }, true)
			}
		}
	}
	check("507 mutation", func(c map[string][]byte) {
		c[m.AcceptedImplicitIndex] = []byte("CREATE TABLE comment_agent_delivery (id uuid);")
	}, true)
	check("third collision", func(c map[string][]byte) {
		c["284_added.up.sql"] = []byte("SELECT 1;")
		c["284_added.down.sql"] = []byte("SELECT 1;")
	}, true)
	check("single numeric alias", func(c map[string][]byte) {
		c["0542_a.up.sql"] = []byte("SELECT 1;")
		c["0542_a.down.sql"] = []byte("SELECT 1;")
	}, true)
	check("numeric alias", func(c map[string][]byte) {
		c["0542_a.up.sql"] = []byte("SELECT 1;")
		c["542_b.up.sql"] = []byte("SELECT 1;")
		c["0542_a.down.sql"] = []byte("SELECT 1;")
		c["542_b.down.sql"] = []byte("SELECT 1;")
	}, true)
	for _, tt := range []struct {
		name, sql string
		bad       bool
	}{
		{"new valid", "CREATE TABLE new_table(id uuid);", false},
		{"new PK", "CREATE TABLE new_table(id uuid PRIMARY\nKEY);", true},
		{"new unique", "ALTER TABLE new_table ADD UNIQUE (id);", true},
		{"nonconcurrent", "CREATE INDEX i ON new_table(id);", true},
		{"concurrent", "CREATE UNIQUE INDEX CONCURRENTLY i ON new_table(id);", false},
		{"mixed", "CREATE INDEX CONCURRENTLY i ON new_table(id); SELECT 1;", true},
		{"transaction", "BEGIN; CREATE INDEX CONCURRENTLY i ON new_table(id); COMMIT;", true},
	} {
		check(tt.name, func(c map[string][]byte) {
			c["542_new.up.sql"] = []byte(tt.sql)
			c["542_new.down.sql"] = []byte("DROP TABLE IF EXISTS new_table;")
		}, tt.bad)
	}
}
