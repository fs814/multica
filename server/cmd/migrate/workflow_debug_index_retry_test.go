package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Only the pre-501 columns needed by the shipped ALTER statements are synthetic.
// The debug tables, defaults, checks and added workflow columns use real migrations.
func workflowDebugFixtureDDL(t *testing.T) string {
	return `CREATE TABLE workflow_run (
 id uuid DEFAULT gen_random_uuid(), workspace_id uuid, accountable_user_id uuid,
 created_at timestamptz DEFAULT now(), status text, template_version_id uuid NOT NULL,
 issue_id uuid, input_instance_id uuid, callback_destination_id uuid, source text);
 CREATE TABLE agent_task_queue (id uuid);` +
		readIndexFixtureMigration(t, "501_workflow_draft_trial") +
		readIndexFixtureMigration(t, "515_workflow_debug_upload")
}

func workflowDebugIndexCases(t *testing.T) ([]concurrentIndexRetryCase, []concurrentIndexDefinitionCase) {
	t.Helper()
	seeds := map[string]string{
		"workflow_execution_snapshot":   `INSERT INTO workflow_execution_snapshot (id,workspace_id,template_id,base_revision,definition,graph_schema_version,definition_hash,environment_snapshot,created_by) VALUES ($id,$id,$id,1,'{}',1,'fixture','{}',$id)`,
		"workflow_debug_quota":          `INSERT INTO workflow_debug_quota (workspace_id) VALUES ($id)`,
		"workflow_debug_policy":         `INSERT INTO workflow_debug_policy (workspace_id) VALUES ($id)`,
		"workflow_debug_task_execution": `INSERT INTO workflow_debug_task_execution (id,workspace_id,run_id,step_id,task_id,task_attempt,runtime_id,daemon_incarnation_id,claim_generation) VALUES ($id,$id,$id,$id,$id,1,$id,$id,1)`,
		"workflow_debug_stop_request":   `INSERT INTO workflow_debug_stop_request (id,workspace_id,run_id,task_id,claim_id) VALUES ($id,$id,$id,$id,$id)`,
		"workflow_debug_cleanup_object": `INSERT INTO workflow_debug_cleanup_object (id,workspace_id,run_id,object_id,object_kind) VALUES ($id,$id,$id,$id,'attachment')`,
		"workflow_debug_upload":         `INSERT INTO workflow_debug_upload (id,workspace_id,run_id,claim_id,task_id) VALUES ($id,$id,$id,$id,$id)`,
		"workflow_run":                  `INSERT INTO workflow_run (workspace_id,accountable_user_id,status,source,execution_mode,execution_snapshot_id,debug_deadline_at,debug_request_hash,debug_policy_revision,debug_retention_seconds,debug_payload_bytes,debug_cleanup_state,debug_purge_after) VALUES ($id,$id,'pending','manual','draft_test',$id,now(),'fixture',1,86400,0,'retained',now())`,
	}
	pattern := regexp.MustCompile(`^CREATE (?:UNIQUE )?INDEX CONCURRENTLY (\w+) ON (\w+) \(([^)]+)\)`)
	var retries []concurrentIndexRetryCase
	var definitions []concurrentIndexDefinitionCase
	for _, number := range []string{"502", "503", "504", "505", "506", "507", "508", "509", "510", "511", "512", "513", "514", "516"} {
		names, err := filepath.Glob(filepath.Join("..", "..", "migrations", number+"_workflow_*.up.sql"))
		if err != nil || len(names) != 1 {
			t.Fatalf("migration %s: %v", number, err)
		}
		version := strings.TrimSuffix(filepath.Base(names[0]), ".up.sql")
		matches := pattern.FindStringSubmatch(readIndexFixtureMigration(t, version))
		if len(matches) != 4 {
			t.Fatalf("unexpected index SQL for %s", version)
		}
		seed, ok := seeds[matches[2]]
		if !ok {
			t.Fatalf("missing table fixture: %s", matches[2])
		}
		seed = strings.ReplaceAll(seed, "$id", "'11111111-1111-4111-8111-111111111111'")
		retries = append(retries, concurrentIndexRetryCase{version, matches[1], matches[2], seed})
		definitions = append(definitions, concurrentIndexDefinitionCase{version, matches[1], matches[2], matches[3]})
	}
	return retries, definitions
}

func TestWorkflowDebugConcurrentIndexRetry(t *testing.T) {
	cases, _ := workflowDebugIndexCases(t)
	testConcurrentIndexRetry(t, cases, workflowDebugFixtureDDL(t))
}

func TestWorkflowDebugIndexRejectsMismatchedDefinition(t *testing.T) {
	_, cases := workflowDebugIndexCases(t)
	testConcurrentIndexDefinitions(t, cases, workflowDebugFixtureDDL(t))
}
