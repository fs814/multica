package migrations

import (
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// These exact R6 full stems are already migration ledger keys. Renaming them
// would replay SQL on upgrades. Freeze membership, including whole-group removal;
// do not extend this list when adding migrations.
var historicalWorkflowMigrationCollisions = map[int][]string{
	232: {"232_channel_media_pending_object_due_index", "232_workflow_template"},
	233: {"233_agent_task_queue_agent_terminal_latest_index", "233_workflow_template_ws_key_index"},
	234: {"234_agent_task_queue_retired_session_id", "234_workflow_template_version_unique_index"},
	235: {"235_chat_message_quick_actions", "235_workflow_run"},
	236: {"236_agent_task_quick_actions_disabled", "236_workflow_run_idempotency_index"},
	237: {"237_quick_action", "237_workflow_run_workspace_created_index"},
	238: {"238_quick_action_workspace_index", "238_workflow_run_active_index"},
	239: {"239_comment_quick_action", "239_workflow_run_issue_index"},
	240: {"240_agent_task_regenerate_quick_actions", "240_workflow_step_run_node_attempt_index"},
	241: {"241_comment_parent_lookup_index", "241_workflow_step_task_unique_index"},
	242: {"242_runtime_profile_add_qoderclicn", "242_workflow_step_run_index"},
	243: {"243_workflow_step_active_index", "243_workspace_teardown_dirty_trigger_guard"},
	244: {"244_issue_dependency_issue_index", "244_workflow_submission_acceptance_event"},
	245: {"245_issue_dependency_depends_on_index", "245_workflow_submission_step_index"},
	246: {"246_inbox_item_issue_index", "246_workflow_acceptance_pending_index"},
	247: {"247_comment_parent_index", "247_workflow_event_idempotency_index"},
	248: {"248_agent_task_trigger_comment_index", "248_workflow_event_run_index"},
	249: {"249_issue_subscriber_delegated", "249_workflow_existing_table_links"},
	250: {"250_agent_task_queue_workflow_step_index", "250_issue_subscriber_opt_out_scope"},
	251: {"251_agent_runtime_unbind", "251_workflow_step_input_node_type"},
	252: {"252_agent_builder_draft", "252_workflow_step_trace_position"},
	253: {"253_runtime_profile_add_qwenpaw", "253_workflow_step_trace_position_index"},
	254: {"254_runtime_profile_add_reasonix", "254_tes85_workflow_acceptance_id_index", "254_tes85_workflow_event_id_index", "254_tes85_workflow_run_id_index", "254_tes85_workflow_run_request_hash", "254_tes85_workflow_step_instance_id_index", "254_tes85_workflow_submission_id_index", "254_tes85_workflow_template_id_index", "254_tes85_workflow_template_version_id_index"},
	255: {"255_agent_task_queue_chat_pending_deferred_v3", "255_tes85_workflow_acceptance_pkey", "255_tes85_workflow_event_pkey", "255_tes85_workflow_run_pkey", "255_tes85_workflow_step_instance_pkey", "255_tes85_workflow_submission_pkey", "255_tes85_workflow_template_pkey", "255_tes85_workflow_template_version_pkey"},
	284: {"284_task_owner_row_fence", "284_workflow_template_revision"},
}

func TestMigrationNumericPrefixesAreUnique(t *testing.T) {
	for _, problem := range migrationPrefixProblems(migrationFilesForLint(t, "*.up.sql")) {
		t.Error(problem)
	}
}

func migrationPrefixProblems(files []string) []string {
	// Migrations through 128 predate uniqueness enforcement. Above that range,
	// only the exact R6 full-stem sets are exempt; these are existing ledger keys.
	const firstUniqueMigrationNumber = 129
	stemsByNumber := make(map[int][]string)
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < firstUniqueMigrationNumber {
			continue
		}
		stemsByNumber[number] = append(stemsByNumber[number], stem)
	}
	var problems []string
	// Iterate the frozen set too: deleting an entire group must not evade lint.
	for number, expected := range historicalWorkflowMigrationCollisions {
		actual := stemsByNumber[number]
		sort.Strings(actual)
		if !reflect.DeepEqual(actual, expected) {
			problems = append(problems, fmt.Sprintf("historical migration prefix %d changed: got %v, want %v; do not replace, delete, or append historical stems", number, actual, expected))
		}
	}
	for number, stems := range stemsByNumber {
		if _, frozen := historicalWorkflowMigrationCollisions[number]; !frozen && len(stems) > 1 {
			sort.Strings(stems)
			problems = append(problems, fmt.Sprintf("migration prefix %d is reused by %v; use a unique new prefix", number, stems))
		}
	}
	sort.Strings(problems)
	return problems
}

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
