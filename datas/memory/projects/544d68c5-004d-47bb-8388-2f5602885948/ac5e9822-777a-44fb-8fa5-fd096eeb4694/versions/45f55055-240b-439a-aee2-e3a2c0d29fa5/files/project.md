# Project

- id: multica-project-map
- topic: Product scope and source navigation
- status: verified
- verified_at: 2026-09-14T20:11:40-07:00
- verified_commit: 238a52b3645249e35b50ccef74dbdf4f6f37a564
- owner_role: Implementation engineer
- sources: [Project identity and binding](sources.md), [Repository rules](../../../../../../CLAUDE.md), [Product overview](../../../../../../README.md), [Workspace manifest](../../../../../../pnpm-workspace.yaml)
- conclusion: Multica organizes human and agent work in a shared task workspace;
  the source is a Go backend and a frontend monorepo with platform-specific apps.
  Verification is source/document inspection, not a feature-completion audit.

## Identity and scope

Repository: `multica-ai/multica`. Match a checkout against the project's current
resource binding before editing; a fork remote alone does not establish that binding.
Project: **multica项目**, UUID `ac5e9822-777a-44fb-8fa5-fd096eeb4694`.
Workspace UUID: `544d68c5-004d-47bb-8388-2f5602885948`.
Live resources bind this checkout through local-directory resource
`c5cfb5d6-4203-4a8f-8339-8303a5bb6118` and repository resource
`6393be40-5685-445e-80ca-8d89a1d81be9` (no fixed ref).
The configured local directory matches this checkout; machine paths are not
portable links. See [Sources](sources.md) for resource verification time and
local-storage authorization, and [Status](status.md) for the task inventory.

The memory scope is reusable knowledge about this repository. Project membership,
delivery stage and feature completion require their own evidence; this module map
does not assert that every documented capability is deployed or accepted.

| Source | Responsibility |
| --- | --- |
| [server](../../../../../../server) | Go backend, routing, persistence and agent execution |
| [apps/web](../../../../../../apps/web) | Next.js web platform |
| [apps/desktop](../../../../../../apps/desktop) | Electron desktop platform |
| [apps/mobile](../../../../../../apps/mobile) | Expo / React Native mobile app; read its own rules before changes |
| [apps/docs](../../../../../../apps/docs) | Documentation site |
| [packages/core](../../../../../../packages/core) | Headless logic, API client, queries and client state |
| [packages/ui](../../../../../../packages/ui) | Atomic UI components |
| [packages/views](../../../../../../packages/views) | Shared web/desktop business views |
| [packages/tsconfig](../../../../../../packages/tsconfig) | Shared TypeScript configuration |
| [packages/eslint-config](../../../../../../packages/eslint-config) | Shared lint configuration |

## Constraints and further reading

Use [CLAUDE.md](../../../../../../CLAUDE.md) for package boundaries, state ownership, database
rules and validation requirements. Read [mobile rules](../../../../../../apps/mobile/CLAUDE.md)
before mobile changes and [conventions](../../../../../../apps/docs/content/docs/developers/conventions.mdx)
before naming or product-copy changes. Existing architecture decisions remain
authoritative in [architecture](../../../../../architecture); plans in [plans](../../../../../plans)
are planning sources, not evidence of completed implementation.

The [workflow authority decision](../../../../../architecture/postgresql-workflow-authority.md)
is linked in place rather than copied here. Use [operations](operations.md) for
verification entry points and the limits of this initialization's checks.
