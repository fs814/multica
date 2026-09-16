# Project Memory

Published source-backed knowledge lives under `projects/<workspace UUID>/<project UUID>/versions/<generation>/`.

- `snapshot.json` is the immutable authoritative snapshot selected by the server binding and verified by SHA-256. Multica reads this snapshot.
- `files/` contains readable copies of that exact generation for Git review. Save through Multica or `multica project memory write/import`, then read back before committing. Do not edit snapshot bytes or the server digest by hand.
- Unpublished candidates and legacy handoff bundles stay in ignored `.multica/`. The server binding selects the current generation; a checkout directory alone does not select a project.

Current project: workspace `544d68c5-004d-47bb-8388-2f5602885948`, project `ac5e9822-777a-44fb-8fa5-fd096eeb4694`.

See [storage contract](../../docs/architecture/project-memory.md).

## Starting the owner

Memory operations run inside the normal Multica daemon; there is no separate
Memory service. `make dev` (also called by Settings `run_multica.ps1` and
`run_multica.sh`) waits for the local API, then starts or reuses that daemon.
The default login profile is `desktop-localhost-<backend port>`, matching Desktop.
Existing owners, their identity, and their task-claim mode are left unchanged.

The startup helper builds a CLI from this checkout when the daemon is stopped,
keeping its executable under `.multica/bin/local-daemon/`. It disables CLI
self-update so an upstream binary cannot replace the local Memory implementation.
If the profile is not authenticated, startup prints the login command; API and web
remain available for sign-in. After login, run `node scripts/ensure-local-daemon.mjs`
with the same `BACKEND_PORT` and optional `MULTICA_PROFILE` used by the launcher.

- `MULTICA_PROFILE`: choose an existing CLI login profile.
- `MULTICA_DEV_DAEMON=0`: start API/web only.
- `MULTICA_DEV_NO_TASK_CLAIMS=1`: start a stopped daemon in maintenance mode;
  registration, heartbeat and Memory remain available, business tasks are disabled.

A running daemon is shared with Desktop and remains running when the web launcher
exits. Stop it explicitly with `multica daemon stop --profile <profile>` when needed.
Starting the local daemon does not change a project's selected owner: Memory bound
to another machine still requires that machine's daemon to be online.
