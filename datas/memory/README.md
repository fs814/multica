# Project Memory

Published source-backed knowledge lives under `projects/<workspace UUID>/<project UUID>/versions/<generation>/`.

- `snapshot.json` is the immutable authoritative snapshot selected by the server binding and verified by SHA-256. Multica reads this snapshot.
- `files/` contains readable copies of that exact generation for Git review. Save through Multica or `multica project memory write/import`, then read back before committing. Do not edit snapshot bytes or the server digest by hand.
- Unpublished candidates and legacy handoff bundles stay in ignored `.multica/`. The server binding selects the current generation; a checkout directory alone does not select a project.

Current project: workspace `544d68c5-004d-47bb-8388-2f5602885948`, project `ac5e9822-777a-44fb-8fa5-fd096eeb4694`.

See [storage contract](../../docs/architecture/project-memory.md).
