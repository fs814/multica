---
name: multica-workflows
description: "Use when listing, validating, starting, inspecting, cancelling, or deciding acceptance for a Multica workflow."
user-invocable: false
allowed-tools: Bash(multica workflow *)
---

# Multica Workflows

The PostgreSQL Workflow Engine is authoritative. CLI and MCP are transport adapters for Workflow Action Contract v1; do not implement orchestration or state transitions outside the server engine.

Use the versioned, closed action inputs:

```bash
multica workflow call template.list --input-json '{"schema_version":"1"}' --output json
multica workflow call template.get --input-json '{"schema_version":"1","template_id":"<id>"}' --output json
multica workflow call template.validate --input-json '{"schema_version":"1","definition":{...}}' --output json
multica workflow call run.start --input-json '{"schema_version":"1","template_id":"<id>","idempotency_key":"<stable-key>","title":"...","description":"...","input":{}}' --output json
multica workflow call run.get --input-json '{"schema_version":"1","run_id":"<id>"}' --output json
multica workflow call run.cancel --input-json '{"schema_version":"1","run_id":"<id>"}' --output json
multica workflow call run.decide_acceptance --input-json '{"schema_version":"1","run_id":"<id>","accept":true}' --output json
```

Persist and reuse `idempotency_key` for retries. Reusing it with changed workflow input returns `idempotency_conflict`. `run.start`, `run.cancel`, and `run.decide_acceptance` mutate durable state; invoke them only when the task requires that side effect. Never place credentials in action JSON, comments, or logs.

To expose the same seven actions over MCP stdio, run `multica workflow mcp serve`. MCP `tools/list` is the source of truth for exact JSON Schemas.

## Saved input instances (web and desktop)

The Run dialog supports named input instances per workspace and template. Save,
update, select or delete an instance without starting a run. Instances store the
input field values, optional project, creator, timestamps, and the displayed
published version ID. The editor's **Save as instance** button opens these controls
with unsaved Input text; incomplete inputs can be saved before publishing.
Loading replaces values exactly, without merging missing keys with node defaults.
A different publication or removed fields requires explicit review; confirming
changes only the current form until **Save changes** is chosen. New runs pin the
displayed published version through the optional X-Workflow-Template-Version-ID
HTTP header, preventing a concurrent publication from changing the submitted
form's graph. The server checks workspace/template ownership and published status.
Required fields, project access, and image references are revalidated before runs.
Edits use the instance revision to reject concurrent overwrites. Existing runs
retain their own input when a saved instance changes or is deleted.

The UI starts each new run with a fresh idempotency key, retaining that key for
retries of the same submission. These instance CRUD endpoints are UI HTTP APIs
under `/api/workflow-templates/{id}/input-instances`, not workflow action names.
See `references/input-instances-source-map.md` for implementation evidence.
## Editing workflow nodes (web and desktop)

Select a canvas node and use **Delete node** in its properties panel, or press
Delete/Backspace while focused inside the canvas. Deletion removes its connected
edges and structured references (branches, rework targets, join sources and routing
source) as one undoable change. Typing in an input field does not delete a node.
Use **Set as entry** to choose a replacement after deleting the entry node. The
first node added to an empty graph becomes its entry. Fix any required connections
or routing reported by Validate, then save the draft; publish to use it for new runs.
Built-in, archived and non-admin views remain read-only. See
`references/editor-source-map.md` for implementation evidence.
Input nodes expose **Name** and **Key information** directly on the canvas. Key
information edits the input node's existing `instruction` field and is saved with
the template draft. Opening Run reuses the input name as its title (or the template
name when blank), and key information as its description. The editor passes its
current input text, so it need not be typed or saved again to submit that run.
The run still pins the published graph and uses its declared fields; this does not
publish draft graph changes. Manual edits and selected input instances override
node defaults. Required custom fields still need values. Saved instances include
the prefilled title and description, and each new dialog starts from node defaults.
The card preserves multiline content and shows it in read-only templates too.
Expand **Configure fields and image** on the card to edit its field declaration or
image. Inline edits use the same undo/redo and Save flow as other graph edits.

Codex workflow steps use a step-bound JSON Schema for their final response.
Return the submission JSON object directly when the runtime requests structured
output; other runtimes keep the delimited submission format. If clarification
is needed, submit verdict blocked and put the exact question in rationale.
A prose question is not a passing result. Run details preserve and display the
original agent reply alongside submission validation errors.
See references/submission-output-source-map.md for the implementation and regression coverage.
