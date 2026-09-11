package migrations

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const maxLegacyMigrationPrefix = 148

// maxLegacyImplicitIndexPrefix freezes the range that predates the
// build-indexes-concurrently rule. The merge with upstream raised this from 272
// to 466: upstream's history runs to 466 and still declares implicit PK/unique
// indexes inline (285, 294, 315, 319, 325, 344, 362, 392, 399), and those
// migrations are already applied in the field, so the rule cannot bind
// retroactively on them. It still binds on everything added after the merge —
// this fork's 470+ range, which complies. Raising this number again to exempt a
// new migration defeats the check; build the index concurrently in its own
// migration instead.
const maxLegacyImplicitIndexPrefix = 466

// legacyDuplicateMigrationStems freezes the historical numeric-prefix
// collisions.
//
// Upstream replaced this whitelist with a flat "every prefix from 129 up is
// unique" rule (TestMigrationNumericPrefixesAreUnique). That rule cannot hold
// in this fork: the workflow runtime occupies 232-253, and every one of those
// numbers also names an unrelated upstream migration. Renaming either side
// would change the schema_migrations version key — which is the FULL stem — and
// make an upgraded database replay non-idempotent CREATE/ALTER statements. So
// the whitelist stays and upstream's stricter test is not adopted; the pairs
// below are frozen, and any THIRD use of one of these prefixes still fails.
var legacyDuplicateMigrationStems = map[string][]string{
	"020": {"020_issue_number", "020_task_session"},
	"026": {"026_comment_reactions", "026_task_messages"},
	"029": {"029_attachment", "029_daemon_token", "029_drop_daemon_pairing"},
	"032": {"032_drop_agent_triggers", "032_issue_search_index", "032_runtime_owner", "032_task_usage"},
	"033": {"033_chat", "033_comment_search_index"},
	"035": {"035_project_priority", "035_task_queue_issue_id_index"},
	"040": {"040_agent_custom_env", "040_chat_unread_since"},
	"041": {"041_agent_custom_args", "041_workspace_invitation"},
	"043": {"043_audit_reserved_slugs", "043_fix_orphaned_autopilot_runs"},
	"046": {"046_agent_mcp_config", "046_agent_unique_name", "046_drop_runtime_usage"},
	"050": {"050_add_onboarded_at_to_users", "050_agent_model", "050_issue_first_executed_at"},
	"060": {"060_add_user_language", "060_agent_description_length", "060_chat_session_runtime_id", "060_issue_origin_quick_create"},
	"065": {"065_backfill_onboarded_at", "065_project_resources"},
	"069": {"069_comment_resolved_at", "069_drop_task_last_heartbeat"},
	"079": {"079_autopilot_run_skipped_status", "079_backfill_api_invalid_request", "079_github_integration"},
	"083": {"083_attachment_chat_columns", "083_runtime_visibility"},
	"084": {"084_squad", "084_task_usage_dashboard_rollup"},
	"091": {"091_autopilot_webhook_triggers", "091_issue_start_date", "091_pr_ci_conflict"},
	"095": {"095_agent_thinking_level", "095_backfill_starter_content_state"},
	"096": {"096_autopilot_squad_assignee", "096_pending_check_suite", "096_user_profile_description"},
	"098": {"098_contact_sales_inquiries", "098_user_onboarding_runtime_choice"},
	"109": {"109_agent_task_waiting_local_directory", "109_drop_agent_skills_local", "109_issue_pull_request_close_intent", "109_lark_integration"},
	"111": {"111_issue_origin_lark_chat", "111_workspace_avatar"},
	"112": {"112_issue_dates_to_date", "112_lark_installation_bot_union_id"},
	"113": {"113_lark_inbound_dedup_per_installation", "113_sys_cron_executions"},
	"120": {"120_autopilot_subscriber", "120_comment_source_task_id", "120_github_pending_installation", "120_runtime_profile"},
	"122": {"122_lark_chat_session_binding_thread_reply", "122_task_handoff_note"},
	"124": {"124_autopilot_run_planned_at", "124_channel_generalization", "124_task_prepare_lease"},
	"127": {"127_issue_pull_request_reference_only", "127_task_squad_id", "127_user_composio_connection"},
	"128": {"128_agent_task_queue_runtime_mcp_overlay", "128_autopilot_collaborator", "128_comment_routing_escalation"},
	// Workflow migrations 232-253 shipped with these prefixes before the
	// repository's uniqueness guard caught the collisions. They may already be
	// recorded in schema_migrations, whose version key is the FULL stem. Renaming
	// them would make an upgraded database replay non-idempotent CREATE/ALTER
	// statements. Freeze the exact pairs instead: fresh databases apply both
	// stems once, upgraded databases skip the already-recorded workflow stem, and
	// any third use of one of these prefixes still fails this test.
	"232": {"232_channel_media_pending_object_due_index", "232_workflow_template"},
	"233": {"233_agent_task_queue_agent_terminal_latest_index", "233_workflow_template_ws_key_index"},
	"234": {"234_agent_task_queue_retired_session_id", "234_workflow_template_version_unique_index"},
	"235": {"235_chat_message_quick_actions", "235_workflow_run"},
	"236": {"236_agent_task_quick_actions_disabled", "236_workflow_run_idempotency_index"},
	"237": {"237_quick_action", "237_workflow_run_workspace_created_index"},
	"238": {"238_quick_action_workspace_index", "238_workflow_run_active_index"},
	"239": {"239_comment_quick_action", "239_workflow_run_issue_index"},
	"240": {"240_agent_task_regenerate_quick_actions", "240_workflow_step_run_node_attempt_index"},
	"241": {"241_comment_parent_lookup_index", "241_workflow_step_task_unique_index"},
	"242": {"242_runtime_profile_add_qoderclicn", "242_workflow_step_run_index"},
	"243": {"243_workflow_step_active_index", "243_workspace_teardown_dirty_trigger_guard"},
	"244": {"244_issue_dependency_issue_index", "244_workflow_submission_acceptance_event"},
	"245": {"245_issue_dependency_depends_on_index", "245_workflow_submission_step_index"},
	"246": {"246_inbox_item_issue_index", "246_workflow_acceptance_pending_index"},
	"247": {"247_comment_parent_index", "247_workflow_event_idempotency_index"},
	"248": {"248_agent_task_trigger_comment_index", "248_workflow_event_run_index"},
	"249": {"249_issue_subscriber_delegated", "249_workflow_existing_table_links"},
	"250": {"250_agent_task_queue_workflow_step_index", "250_issue_subscriber_opt_out_scope"},
	"251": {"251_agent_runtime_unbind", "251_workflow_step_input_node_type"},
	"252": {"252_agent_builder_draft", "252_workflow_step_trace_position"},
	"253": {"253_runtime_profile_add_qwenpaw", "253_workflow_step_trace_position_index"},
	// Fork collisions frozen when this fork merged upstream/main. This fork and
	// upstream both numbered from the same base independently, so 21 prefixes
	// ended up carrying one migration from each side. Frozen rather than
	// renumbered for the same reason as 232-253 above, and for one more that is
	// specific to these: renumbering was attempted and it BROKE ordering —
	// 449_issue_pool_schema creates the issue_pool_* tables that the 20
	// migrations at 470-490 then ALTER, and 274_workflow_callback_destination_*
	// is a prerequisite of 280. Moving the creating migration above its
	// dependents made `sqlc generate` fail with `relation "issue_pool_policy"
	// does not exist`. The runner keys schema_migrations on the FULL stem, so
	// both members of a pair apply exactly once and relative order is preserved.
	"273": {"273_agent_task_queue_runtime_id_index", "273_workflow_external_handoff"},
	"274": {"274_task_token_workspace_id_index", "274_workflow_callback_destination_name_index"},
	"275": {"275_task_token_agent_id_index", "275_workflow_callback_delivery_event_index"},
	"276": {"276_chat_draft_restore_task_id_index", "276_workflow_callback_delivery_claim_index"},
	"277": {"277_autopilot_run_task_id_index", "277_workflow_callback_delivery_run_index"},
	"278": {"278_agent_task_queue_agent_id_keyset_index", "278_workflow_callback_destination_primary_index"},
	"279": {"279_agent_task_queue_issue_id_keyset_index", "279_workflow_callback_delivery_primary_index"},
	"281": {"281_agent_workspace_id_keyset_index", "281_workflow_callback_delivery_primary_key"},
	"282": {"282_issue_workspace_id_keyset_index", "282_runtime_profile_add_knot"},
	"283": {"283_agent_runtime_workspace_id_keyset_index", "283_runtime_profile_add_knot_http"},
	"284": {"284_task_owner_row_fence", "284_workflow_template_revision"},
	"449": {"449_autopilot_trigger_created_by", "449_issue_pool_schema"},
	"450": {"450_drop_comment_delegated_failure_pending_index", "450_issue_pool_policy_id_index"},
	"451": {"451_agent_task_comment_thread", "451_issue_pool_cycle_id_index"},
	"452": {"452_agent_task_pending_thread_unique", "452_issue_pool_item_id_index"},
	"453": {"453_drop_pending_issue_agent_unique", "453_issue_pool_policy_autopilot_index"},
	"454": {"454_drop_comment_content_bigm_index", "454_issue_pool_cycle_idempotency_index"},
	"455": {"455_drop_comment_content_trgm_index", "455_issue_pool_item_active_issue_index"},
	"456": {"456_cancel_comment_assignee_fallbacks", "456_issue_pool_cycle_autopilot_index"},
	"457": {"457_issue_pool_item_cycle_index", "457_task_message_output_truncated"},
	"458": {"458_agent_task_cancellation_actor", "458_issue_pool_primary_keys"},
}

var migrationPrefixPattern = regexp.MustCompile(`^(\d+)_`)

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
	stemsByPrefix := migrationStemsByPrefix(t)

	for prefix, stems := range stemsByPrefix {
		sort.Strings(stems)

		legacyStems, isLegacyDuplicate := legacyDuplicateMigrationStems[prefix]
		if isLegacyDuplicate {
			expected := append([]string(nil), legacyStems...)
			sort.Strings(expected)
			if !reflect.DeepEqual(stems, expected) {
				t.Errorf("legacy duplicate migration prefix %s changed: got %v, want %v; do not add to or rename historical duplicate-prefix migrations", prefix, stems, expected)
			}
			continue
		}

		if len(stems) > 1 {
			t.Errorf("migration prefix %s is reused by %v; use the next unique prefix instead", prefix, stems)
		}
	}
}

func TestNewMigrationPrefixesStartAfterLegacyRange(t *testing.T) {
	stemsByPrefix := migrationStemsByPrefix(t)

	for prefix, stems := range stemsByPrefix {
		n, err := strconv.Atoi(prefix)
		if err != nil {
			t.Fatalf("parse migration prefix %q: %v", prefix, err)
		}
		if n <= maxLegacyMigrationPrefix && !isKnownLegacyPrefix(prefix) {
			t.Errorf("migration prefix %s is in the frozen legacy range 001-%03d: %v; new migrations must start at %03d", prefix, maxLegacyMigrationPrefix, stems, maxLegacyMigrationPrefix+1)
		}
	}
}

func TestNewMigrationsDoNotCreateImplicitIndexes(t *testing.T) {
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
		if prefix <= maxLegacyImplicitIndexPrefix {
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
			if strings.Contains(sql, "UNIQUE") && !strings.Contains(sql, "CREATE UNIQUE INDEX CONCURRENTLY") {
				t.Errorf("%s:%d creates an implicit unique index; use CREATE UNIQUE INDEX CONCURRENTLY in its own migration", filepath.Base(file), lineNumber+1)
			}
		}
	}
}

func migrationStemsByPrefix(t *testing.T) map[string][]string {
	t.Helper()

	files := migrationFilesForLint(t, "*.up.sql")
	stemsByPrefix := make(map[string][]string)
	for _, file := range files {
		stem := strings.TrimSuffix(filepath.Base(file), ".up.sql")
		match := migrationPrefixPattern.FindStringSubmatch(stem)
		if match == nil {
			t.Fatalf("migration %s does not start with a numeric prefix followed by underscore", stem)
		}
		stemsByPrefix[match[1]] = append(stemsByPrefix[match[1]], stem)
	}
	return stemsByPrefix
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

func isKnownLegacyPrefix(prefix string) bool {
	if _, ok := legacyDuplicateMigrationStems[prefix]; ok {
		return true
	}

	switch prefix {
	case "001", "002", "003", "004", "005", "006", "007", "008", "009", "010",
		"011", "012", "013", "014", "015", "016", "017", "018", "019", "021",
		"022", "023", "024", "025", "027", "028", "030", "031", "034", "036",
		"037", "038", "039", "042", "044", "045", "047", "048", "049", "051",
		"052", "053", "054", "055", "056", "057", "058", "059", "061", "062",
		"063", "064", "066", "067", "068", "072", "073", "074", "075", "076",
		"077", "078", "080", "081", "082", "085", "086", "087", "088", "089",
		"090", "092", "093", "094", "097", "100", "101", "102", "103", "104",
		"105", "106", "107", "108", "110", "114", "115", "116", "117", "118",
		"119", "121", "123", "125", "126", "129", "130", "131", "132", "133",
		"134", "135", "136", "137", "138", "139", "140", "141", "142", "143",
		"144", "145", "146", "147", "148":
		return true
	default:
		return false
	}
}
