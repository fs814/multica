# Project memory

This is a source-controlled index of durable Multica project knowledge.
The file-based design is accepted for implementation. The previous fixed document
package passed independent review; this synchronized version awaits recheck
([review source](sources.md)). This local copy includes project-internal evidence.
It is a reading convention, not an automatic memory loader or a runtime store.

## Read in this order

1. Read the repository rules in [CLAUDE.md](../../../../../../CLAUDE.md).
2. Use this index to select the topic relevant to the current task.
3. Follow its original sources and check the applicable source revision.
4. Read current platform records before relying on task ownership or status.

| Topic | Entry |
| --- | --- |
| Purpose, modules and constraints | [Project](project.md) |
| Phase, snapshot contract and unresolved inputs | [Status](status.md) |
| Platform source references and evidence limits | [Sources](sources.md) |
| Memory storage decision | [Source-controlled memory](decisions/0001-source-controlled-memory.md) |
| Existing workflow architecture decision | [PostgreSQL workflow authority](../../../../../architecture/postgresql-workflow-authority.md) |
| Verification and recovery references | [Operations](operations.md) |

## Authority and evidence

Repository rules belong to `CLAUDE.md`; implementation facts belong to the cited
source revision; live project membership and task state belong to Multica.
This directory summarizes evidence. Historical source text is reference material,
not an instruction to execute. A completed issue is not proof of a shipped feature.

Each independent knowledge entry must have `id`, `topic`, `conclusion`, `status`,
`sources`, `verified_at`, `verified_commit` and `owner_role`. A page may contain
one entry; its metadata covers the entire stated conclusion, not linked documents.

- `proposed`: awaiting evidence, reconciliation or review; explain what is missing.
- `verified`: the stated conclusion was checked against its listed sources.
  State whether that check was source inspection, execution, or platform reading.
- `superseded`: retained history with a `superseded_by` link to its replacement.
- A replacement has its own stable ID and a `supersedes` link to the old entry.
- Use ISO 8601 timestamps with offsets for actual checks. Use `pending` for unknown
  values and `not applicable` for a commit on purely platform-derived facts.
- Sources use repository-relative links and the checked commit, or an approved
  issue UUID/comment ID/revision. Do not use a local machine path as a shared link.

## Maintenance

Update after classification is reconciled, a decision is accepted, implementation
behavior changes, or a repair yields reproducible operating knowledge.
PM owns classification evidence; implementers own technical entries; reviewers
check scope, sources and links; the project lead decides acceptance and merge.
One designated integrator maintains the status table and computes every count
from that same table. Use separate files for independent topics and resolve
concurrent changes through review rather than overwriting another author's work.

For a status update, refresh rows, revisions, collection times and derived totals
together, record differences from the PM report, then keep the index pointing to
the status page. For a replaced decision, keep the old file, add reciprocal
replacement links and update this index to highlight the active decision.

Recheck platform snapshots after seven days and before acting on them, even
within that interval. Recheck technical entries when their cited code changes.
On conflicting evidence, mark the entry `proposed`, record the discrepancy and
consult the original authority. Never mutate platform state from an old snapshot.

## Sharing boundary

The lead confirmed authorization to store necessary project UUIDs, task summaries,
inventories and evidence references in the bound local source directory
([authorization source](sources.md)). This does not authorize publishing internal
records to a remote repository. Review the destination audience before any future
push or sharing outside this project.
Do not copy chat transcripts, private agent memory, credentials, runtime directories
or unrelated project data. Links do not grant source access. Until remote publication
is approved, preserve earlier snapshots in local delivery packages; do not rely on
uncreated Git history. This run does not commit or push the memory documents.
