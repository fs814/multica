## Knot HTTP remote workspace

When Knot HTTP dispatches without a client UUID (including `client_uuid=remote`
or a failed local-client probe), the request omits the dispatching machine's
workspace path. The selected remote tool host uses its own filesystem; a local
Multica checkout is not copied to it. An explicitly resolved client UUID retains
the configured workspace path. Do not assume local files exist on a remote host.

Ordinary Knot HTTP issue assignments are the one exception to the
agent-managed-status rule above: they receive their title and description
inline, loaded by the daemon from the assigning server using task-scoped auth,
and return their result directly (the server's completion fallback records it as
an issue comment). For these the server advances the issue from `todo`/`backlog`
to `in_progress` on start, then to `in_review` on completion with nonempty
output, in the same transaction as the task transition. Those updates preserve
terminal states, reassignment and manual status changes, and broadcast
`issue:updated` without enqueueing another run. Leaders and squad tasks retain
their own lifecycle. This path does not run remote Multica CLI setup, and a
failed context read fails the run. Comment-triggered turns, explicit handoffs,
workflows, chats, autopilots and quick-create keep their existing contracts.

## Interrupted local execution

Provider capacity/rate-limit failures are retried with 30/60-second cooldowns,
within the task's configured attempt budget and a hard maximum of three.
`max_attempts=1` disables retry; autopilot run-only executions keep their own
scheduling semantics. `Selected model is at capacity` is not a missing model.
Inspect the newest run before retrying manually: an earlier failed delegated
run and a queued/deferred coordinator recovery can coexist. Do not create
duplicate runs or silently switch the configured model.

If the local daemon stops while a task is executing, it reports `runtime_offline`
with the available session and work directory so normal server retry rules can
resume it after the runtime reconnects. This differs from an explicit
server/user cancellation, which is acknowledged without retrying. A completed
result remains completed even if daemon shutdown begins at the same time.
Historical failed runs remain in the issue's run history; inspect the latest run
for recovery progress.
