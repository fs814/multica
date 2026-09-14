# Editor publication and run history

The editor publishes the revision and draft version the author reviewed. The UI
sends `revision` and `draft_version_id` together to the HTTP publish endpoint;
a changed revision or draft returns HTTP 409 and preserves the working graph.
Save-then-publish uses the save response's preconditions. Installed clients may
still send an empty publish body; the server reads, validates and publishes in
one transaction, but these legacy calls do not confirm a previously viewed draft.
This UI endpoint is not a new Action Contract action.

Canvas / Instances / Run history and list filters/pages are encoded in the URL.
Run history displays the run's immutable published graph and actual input keys.
Node badges reflect observed attempts; edges do not claim an executed path.
If the pinned version cannot be loaded, the linear attempt trace remains usable.

## Validation and typed authoring

Validation responses retain `valid` and `messages`. Validation and save/publish
rejections also return additive `diagnostics`: `code`, `message`, `field_path`,
and, when identifiable, `node_key` and `edge_id`. Pointers address the definition
that was checked; never infer a node from translated or English error text.
Unknown codes and older servers remain readable through `messages`. The editor
selects and brings a diagnosed node into view, opening its properties panel.

Graph v2 input ports offer a source-output picker with type labels. Incompatible
sources are disabled; combined control/data cycles and other graph rules remain
server-authoritative. Confirm a safe input-port rename to atomically rewrite its
data edges and owned condition predicates. Output IDs and input IDs coupled to an
output value are fixed producer contracts and cannot be renamed by this control.
Typing a proposed name does not rename the port.
One Undo restores the complete pre-rename graph. Join nodes wait only for activated
predecessors. These controls do not convert graph v1 to v2.

## Comparing and upgrading versions

Compare published versions, or a published version with the working draft, using
immutable version reads. The comparison summarizes node, binding, entry, limits,
format and input-field changes, including added/removed fields, types and options.
It never changes an instance binding or an existing run.

Review an instance upgrade before applying it. Each unknown input key requires an
explicit Keep or Remove choice. Applying changes only the local edit; Save changes
persists the new binding and reviewed values. Keeping an unknown value preserves
it for review, but existing engine rules block running until unknown fields are
explicitly removed. Historical runs keep their original version and inputs.

## Draft trials (default disabled)

When deployment readiness and the workspace's administrator setting allow it,
Test draft executes the current working graph without publishing or saving it.
Apply or discard unapplied JSON first. Confirm the input, final image, project,
execution limits and real execution warning on each new trial. Trials consume
agent capacity and can modify external resources; they are not a sandbox.

The result panel and history render the immutable execution snapshot. Closing the
panel preserves the editor's working graph and dirty state. Ordinary runs and
input instances keep their existing version bindings. Trial APIs live under
`workflow-test-runs`; do not pass a trial ID to the published-run action surface.

Cancellation ends logical workflow progress, while process stop and delivery
confirmation can remain pending. Retention cleanup waits for those confirmations
and settled uploads. Expired details show a metadata tombstone; late writes cannot
restore removed content. Deleting trial records never rolls back files, commits,
or other external effects. Disabling new trials leaves existing recovery active.
