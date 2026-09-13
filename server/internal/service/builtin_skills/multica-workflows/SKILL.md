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

**Save as instance** opens a dedicated create dialog with Cancel / Create instance.
It captures current unsaved Input text, the declared form and instance-level image
reference. Saving never creates an issue, run or agent task. Incomplete inputs and
unpublished declarations can be saved; the latter remain pending version binding.

Use **Workflow instances** in the workflow area, or the workflow detail's
Canvas / Instances / Run history tabs. Instances have durable URLs at
/{workspace}/workflow-instances/{id}. Their detail page edits inputs, explicitly
saves changes, duplicates, archives/restores and lists independent run history.

**Run saved inputs** sends only instance identity, expected revision and a retry
key; the engine locks and reads the saved snapshot in its StartRun transaction.
**Run these edits only** sends the complete temporary snapshot without updating
the instance. **Rerun this snapshot** uses that historical run's version, input
and resource references. Each new execution gets a fresh key; network retries
reuse it. Historical input and provenance never change with instance updates.

An instance stays on its original published version after a new publication.
Upgrading is explicit: review old/new fields and input mode, preserve or explicitly
remove obsolete values, then save the version binding. Unknown fields, incomplete
required input, invalid options and inaccessible resources block execution.
Image uploads store attachment IDs, never signed URLs. Instance updates use
revision compare-and-swap; conflicts preserve the editor's values.
Archiving stops new starts and preserves history, without cancelling existing runs.

The workspace-scoped UI APIs are /api/workflow-instances (list, detail,
validation, run, history, archive/restore), with create/update under
/api/workflow-templates/{id}/input-instances and immutable version reads under
/api/workflow-templates/{id}/versions/{versionID}. These are HTTP UI endpoints,
not new workflow action names.

## Editing workflow nodes (web and desktop)

Select a canvas node and use **Delete node** in its properties panel, or press
Delete/Backspace while focused inside the canvas. Deletion removes its connected
edges and structured references (branches, rework targets, join sources and routing
source) as one undoable change. Typing in an input field does not delete a node.
Use **Set as entry** to choose a replacement after deleting the entry node. The
first node added to an empty graph becomes its entry. Fix any required connections
or routing reported by Validate, then save the draft; publish to use it for new runs.
Built-in, archived and non-admin views remain read-only.
Input nodes expose **Name** and **Key information** directly on the canvas. Key
information edits the input node's existing `instruction` field and is saved with
the template draft. Opening Run reuses the input name as its title (or the template
name when blank), and key information as its description. The editor passes its
current input text, so it need not be typed or saved again to submit that run.
The run still pins the published graph and uses its declared fields; this does not
publish draft graph changes. Manual edits in the Run dialog override node defaults. Instance pages use their
own saved inputs without merging node defaults. Required custom fields still need values. Saved instances include
the prefilled title and description, and each new dialog starts from node defaults.
The card preserves multiline content and shows it in read-only templates too.
Expand **Configure fields and image** on the card to edit its field declaration or
image. Inline edits use the same undo/redo and Save flow as other graph edits.
The workspace sidebar places **Workflow instances** directly below **Workflows**.
The workspace instance page groups each page of results by parent workflow ID,
with links to both the parent workflow and individual instances. Search, workflow
filtering, archive visibility and pagination remain available. The template's
own instance list remains scoped to that workflow.

Codex workflow steps use a step-bound JSON Schema for their final response.
Return the submission JSON object directly when the runtime requests structured
output; other runtimes keep the delimited submission format. If clarification
is needed, submit verdict blocked and put the exact question in rationale.
A prose question is not a passing result. Run details preserve and display the
original agent reply alongside submission validation errors.

## Graph definition v2

The graph's numeric `schema_version: 2` is independent of Action Contract v1
and submission `schema_version: 1`. Existing v1 definitions retain their legacy
scheduler and bounded rework behavior; never silently upgrade or downgrade them.
New manually created graphs use v2. Imported, generated and duplicated graphs
retain their declared version.

V2 separates ordinary flow (`next` plus parallel `next_ids`) and typed
`data_edges`. Flow activates work; data only binds values. Conditions evaluate
ordered predicates, select the first match and require exactly one default.
Joins wait for every activated predecessor; inactive branches are skipped.
Required data is checked before dispatch. Final failure skips dependents while
independent work finishes. Agent retries require explicit `max_attempts` (total
attempts including the first); default is one. Acceptance rejection fails its
branch without a rework target when `can_reject_without_rework` is true.

Incomplete v2 drafts can be saved, but publishing and starting validate the
complete graph. Combined control/data cycles, invalid ports, duplicate IDs and
ambiguous collection bindings are rejected. Running instances keep their pinned
published version.

For editor publication preconditions, URL state and pinned run graphs, see [references/editor.md](references/editor.md).
