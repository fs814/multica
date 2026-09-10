# Graph definition v2 source map

- `server/internal/workflow/definition.go`, `graph_v2.go`: versioned ports, stable
  edge IDs, type/cardinality/activation validation and deterministic planning.
- `graph_v2_engine.go`: executes decisions under the run row lock; persists all
  attempts, inactive/dependency skips, missing-input failures and terminal events.
- `commands.go`, `reconciler.go`: submit, retry, acceptance and reconciliation
  enter the same version-specific scheduler. V1 behavior remains in its old path.
- `server/internal/handler/workflow_template.go`: incomplete draft validation,
  authoritative complete publish validation and schema metadata preservation.
- `packages/core/workflows/graph-v2.ts`: editor-side structural validation.
- `packages/views/workflows/graph/connect.ts`: atomic connect/reconnect; invalid
  drags preserve the previous graph. `editor-state.ts` handles undo and removal.
- `packages/views/workflows/editor/ports-section.tsx`: input/output types,
  required/collection settings, ordered predicates and explicit retry count.
- `packages/views/layout/theme-toggle.tsx`: shared theme switch. The existing
  root ThemeProvider persists selection and defaults to the operating system.

## Wire rules

Each `next[i]` has a globally unique `next_ids[i]`; branch `id` and data edge `id`
share that namespace. A data edge is `{id,source,source_port,target,target_port,order}`.
Ports declare `{id,type,required?,multiple?}`; types are string, number, boolean,
object, array, any. Connections require equal types or an `any` input. A normal
input takes one source. A collection input receives a list sorted by distinct,
nonnegative `order` values. Explicit data bindings appear as `bound_inputs` in
step context and in the task's Connected inputs prompt.

Ports address existing top-level output values: input nodes expose run input;
agent nodes expose submitted artifact `type`, `summary`, `references`; condition
nodes expose `target` and `verdict`; joins and other synchronous passthrough nodes
expose their bound inputs. Declaring a port does not synthesize its value. Missing
or incompatible required values fail before dispatch; optional values are omitted.

A condition branch contains either `predicate:{input_port,equals}` or legacy
`when_verdict`, or neither for the default. Predicates compare JSON values;
non-default branches are evaluated in stored order. New v2 failure never routes
through a failure condition: final failure propagates to dependent nodes.
A condition may compare a bound `verdict` input; without one it uses `pass`.

An upstream routing source must be a direct ordinary control predecessor in v2,
so agent selection cannot create a hidden scheduling dependency. Static path
analysis is bounded to 4096 condition combinations; larger graphs are rejected
with an explicit validation error. No migration rewrites old definitions. To
return to v1 behavior, use the original published v1 template/version, not a
version-number edit on a v2 graph. Existing runs remain pinned during publication.

## Verification

`graph_v2_test.go` covers parallel/conditional activation, joins, data types,
missing inputs, retry dependencies, ordering and cycle rejection.
`graph_v2_integration_test.go` uses PostgreSQL for concurrent replay, retries,
independent failure completion, pinned versions and acceptance without rework.
`workflow_graph_v2_test.go` checks incomplete save, publish rejection, completed
save/publish/load and persisted schema version. Views graph/editor/canvas/run
regressions cover connection edits and version-specific acceptance controls.
