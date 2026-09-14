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
Condition inputs cannot be renamed from or to `verdict`: the engine consumes that
key implicitly. Both old and new names must preserve execution contracts.
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

## Directory script pipelines

Choose **Directory scripts** on the Input node. Set an absolute directory on the
execution machine, choose auto/Windows/macOS/Linux, and select clone, build, run,
or any subset. Selected stages always execute in that order. Connect the Input
to exactly one Agent node: its assigned runtime determines the execution machine.
That first agent step executes scripts directly, without an LLM or provider login;
subsequent agent nodes can review the recorded result normally.

Windows runs `.ps1` with PowerShell 7; macOS/Linux run `.sh` with Bash. Paths may
be explicit (absolute or relative to the directory) or discovered from stage names.
Conventions include `build_<project>_clone`, `build_<project>`, and `run_<project>`.
Ambiguous matches require an explicit path. All selected files are checked before
any stage starts. The directory is also the process working directory. Desktop
folder picking selects a local path; enter remote paths for remote runtimes.
With no directory entered, the picker starts at `C:\sourcecode\Settings\winbuild`
on Windows, `~/sourcecode/Settings/macbuild` on macOS, and
`~/sourcecode/Settings/linuxbuild` on Linux. The desktop resolves `~` to the
current user's home directory. An existing directory value takes precedence.

Input settings are defaults for runs and are saved with each input instance.
Instance values override the defaults, including cleared explicit script paths.
The input keys are `script_directory`, `script_platform`, `script_steps` (a JSON
array encoded as a string), `clone_script`, `build_script`, `run_script`, and
`script_timeout_seconds`. Node defaults are in `script_pipeline`.

Save and publish before running; draft trials do not execute directory scripts.
An instance can be created before publication, but running requires binding it
to a published version with the same input mode and a configured execution agent.
Use **Configure and publish workflow**, then **Bind published version**, review
the input, and **Save changes**. Reserved directory-script input keys are retained
when binding. **Run saved inputs** uses the saved revision; **Run these changes
once** uses an execution snapshot without overwriting that revision. Save and
temporary-run buttons become available after editing an input.

Finished instance runs offer **Run again** in the run detail header. Failed,
completed, and cancelled runs can start a new run with the original input snapshot
and published workflow version. This reruns the whole workflow, not only its
failed step. **Edit inputs and run** opens the source instance to change inputs
or select a subset of directory stages. Neither action changes the old run.
Archived instances must be restored first.

Directory-script instances and finished instance runs also provide **Clone only**,
**Build only**, and **Run only**. Instance buttons use the current edited inputs;
run-detail buttons use that historical run's inputs and published version. Each
creates a new run with only the selected script stage, preserving the instance's
saved stage selections. Other workflow nodes still follow the published graph.
The instance run API accepts optional `script_step` (`clone`, `build`, or `run`)
with saved, temporary, or history mode. The server applies this only to the new
execution snapshot and rejects it for non-script inputs.

Scripts must exit to complete their stage. For a desktop application, a run script
can launch the built executable, verify startup, and exit successfully while the
application remains open. A development server that stays in the foreground keeps
the stage running until it exits, is cancelled, or reaches the timeout.
Upgrade the execution daemon to advertise `workflow_script_pipeline_v1`.
An incompatible platform or an older daemon is blocked during routing.
Execution stops on the first failed stage. Logs and exit codes become a structured
workflow submission so the engine applies its normal failure policy. A total
timeout (default one hour, maximum one day), cancellation of child processes,
bounded log buffers, and serialization for the same directory limit resource use.

## Removing input instances

The workspace instance list, template instance list, and instance detail header
provide **Delete instance** with a confirmation naming the instance. This uses
the existing recoverable delete API: it removes the instance from active lists
by archiving it. **Include archived** and **Restore** recover it. Existing runs
retain their input snapshots, results, and source metadata. Lists refresh after
success; a deleted detail page returns to its list. Failed requests stay open
for retry and do not navigate away.
