# Source-controlled project memory

- id: multica-memory-0001
- topic: Durable project knowledge storage
- status: verified
- verified_at: 2026-09-14T19:43:45-07:00
- verified_commit: 238a52b3645249e35b50ccef74dbdf4f6f37a564 (supporting source inspection)
- owner_role: Architect; project lead accepts, implementation engineer maintains
- sources: [Proposal and lead acceptance](../sources.md), [memory contract](../README.md), [root rules](../../../../../../../CLAUDE.md), [project context writer](../../../../../../../server/internal/daemon/execenv/context.go), [ignore rules](../../../../../../../.gitignore), [Codex memory policy](../../../../../../../server/internal/daemon/execenv/codex_memory.go), [Hermes memory store](../../../../../../../server/internal/daemon/execenv/hermes_memory.go)
- conclusion: Use Markdown under `docs/memory/`, reached from root `CLAUDE.md`,
  for project knowledge. The lead accepted this file-based design for the current
  implementation in comment `01a0a2f0-9ea6-7658-91ff-6fea0d597520`, revision 1.
  Verification means reading that acceptance record, not passing independent
  document review. The previous fixed document package later passed independent
  review (S8 in the source register); this synchronized version's recheck and
  final task acceptance remain pending.
- supersedes: none

## Context and alternatives

Knowledge needs a durable, inspectable home beside the implementation it describes.
The following supporting facts were inspected on 2026-09-14 at the recorded commit:

| Option | Evidence and tradeoff |
| --- | --- |
| Source-controlled Markdown (selected) | Explicit reading, diffs and history; depends on team maintenance and matching repository versions |
| Generated project context | `writeProjectResources` writes `.multica/project/resources.json`; its sidecar manifest supports cleanup and `.multica/` is ignored, so it is not the knowledge archive |
| Native agent memory | Codex's managed policy disables native memory by default; Hermes overlay memory is keyed by profile and agent, with documented host-passthrough exceptions; neither defines this project knowledge contract |
| New platform storage service | Could support broader retrieval and access controls, but requires a separate design and implementation scope |

## Consequences

The root entry points to one index; topics carry sources and explicit verification
state. Existing architecture decisions are linked rather than redefined. A filename
does not ensure loading by every runtime. This choice does not change runtime
memory configuration, databases, APIs or UI.

Platform facts require live verification. Internal evidence is authorized locally;
remote publication requires a separate sharing decision.
Cross-machine use requires the same repository content. Status snapshots expire
for reliance after seven days; technical knowledge is rechecked on source changes.

## Acceptance and replacement

The design acceptance source is recorded above. Independently review evidence,
links, sharing scope and reconciliation before final delivery acceptance; record
that review separately without backdating it to design acceptance.
If replaced, retain this file as `superseded`, add a
`superseded_by` link, give the replacement a `supersedes` link, and update the
[index](../README.md) to highlight the replacement. Do not create a fictional
replacement just to demonstrate the procedure.
