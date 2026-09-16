# Source register

- id: multica-memory-sources
- topic: Provenance and verification limits
- status: verified
- verified_at: 2026-09-14T20:11:40-07:00
- verified_commit: 238a52b3645249e35b50ccef74dbdf4f6f37a564 (repository facts only)
- owner_role: Implementation engineer; PM owns classification evidence
- sources: Authenticated Multica CLI reads listed below; [repository rules](../../../../../../CLAUDE.md)
- conclusion: References below identify observed records and attributed summaries.
  Reading a report does not independently verify every claim in that report.

## Task sources

All S1–S8 are comments on TES-103, issue `01a0a2e2-69bd-7978-a3ef-daf2f3b6f9f4`.
Their comment revision is **1**; the thread root is
`01a0a2e4-ac00-7c87-b533-db4e5b00929b`.
Read with `multica issue comment list <issue-id> --thread <root-id> --tail 30 --compact --output json`;
scan `--roots-only --summary --compact` before selecting other threads.

| Source | Comment ID | Published at | Meaning |
| --- | --- | --- | --- |
| S1 | 01a0a2e7-dea9-716e-ab1c-ea511d024e21 | 2026-09-14T19:31:39-07:00 | Architect proposes file-based memory and evidence contract. |
| S2 | 01a0a2ea-c86b-7364-b283-6e9b022a4cf3 | 2026-09-14T19:34:50-07:00 | PM reports 7 original, 75 moved, 16 deferred. |
| S3 | 01a0a2ef-ff4b-7863-8ead-6773381c9ac3 | 2026-09-14T19:40:32-07:00 | Independent classification review agrees on current set, disputes TES-71/77/93, limits historical proof. |
| S4 | 01a0a2f0-9ea6-7658-91ff-6fea0d597520 | 2026-09-14T19:41:13-07:00 | Lead accepts file-based implementation and local UUID/summary/evidence storage; remote publication is excluded. |
| S5 | 01a0a2f1-4d1e-76fe-9021-d0f9deb87b22 | 2026-09-14T19:41:58-07:00 | Lead assigns PM classification corrections; task remains in progress. |
| S6 | 01a0a305-2c10-76f4-9cf7-0b00f6ecabd2 | 2026-09-14T20:03:40-07:00 | PM corrects TES-77/93 membership, retains TES-71 pending; 84 project, 14 outside, 15 unresolved. |
| S7 | 01a0a305-cd36-7baf-8470-56f8c20975c4 | 2026-09-14T20:04:21-07:00 | Lead assigns this synchronized local memory delivery and continued in-progress status. |
| S8 | 01a0a30a-8f4e-7a12-a078-6d2da82e8a27 | 2026-09-14T20:09:33-07:00 | Previous 9-file package passes independent review; lists required synchronization checks. |

S2 attachments downloaded with `multica attachment download <id> -o <workdir>`:
`01a0a2ea-c858-72cd-b2f3-8925d4b92b38` (classification CSV) and
`01a0a2ea-c860-7150-80ad-a4da881681c4` (project report).
The original CSV contains no historical source revision or per-row collection
time. Its old project and moved/retained labels are PM assertions. Current field
agreement does not prove only project_id changed throughout the entire operation
or independently prove every mutation used `--no-start`.

## Live source collection

Interval: 2026-09-14T20:11:40-07:00 — 2026-09-14T20:11:40-07:00; project UUID
`ac5e9822-777a-44fb-8fa5-fd096eeb4694`.
Commands: `multica project get <uuid> --output json`,
`multica project resource list <uuid> --output json`,
`multica issue list --project <uuid> --limit 100 --offset 0 --output json`,
and the same inventory without project filter for set reconciliation.
Per-row source revision/time is in [issues.csv](issues.csv) and
[classification-pending.csv](classification-pending.csv). Project/resource payloads
do not expose a revision; no value was invented. Local resource matches the bound
checkout, GitHub resource is `https://github.com/multica-ai/multica`, no fixed ref.
These checks do not require or imply publishing to that repository.

## Representative evidence

Direct issue/comment checks completed by 2026-09-14T19:48:53-07:00. Inventory timestamps
remain the original collection times; report-derived claims are labeled below.

- **Completed — TES-20**, UUID `7803b711-d3d8-4ab6-8e7d-243b8a3d6ba0`:
  issue revision 1, done. Direct issue and thread read confirmed user comment
  `b4a58128-9ccc-4ac8-b0c6-667d5d23c413`, revision 1, at
  2026-08-14T05:15:31-07:00, under root `e93eea65-417f-4374-a313-4822dd58a8ee`.
  It confirms the user could invoke TClaude; it does not establish TCodex, merge,
  deployment or later runtime behavior.
- **Active — TES-103**: current revision/status in the inventory; S4 explicitly
  authorizes this implementation and reserves independent review to the lead.
- **Corrected — TES-77**, UUID `5f514b39-96ed-4dbf-a3cc-1f2b634e114f`:
  now in project, live revision 2, medium classification confidence.
  Prior direct read of comment `2a1e7a1a-3217-4dae-806e-9cb13261201d`, revision 1,
  established troubleshooting content. S6 now resolves placement while preserving
  uncertainty about original purpose and product repair.
- **Corrected — TES-93**, UUID `ac7600fb-8ea7-45f4-9054-1b4bb8f810c1`:
  now in project, live revision 5, high classification confidence.
  Prior direct description read and S6 comment/run metadata support run-only
  lifecycle relevance. S6 cites comment `01a0a00c-8456-7176-b903-82f73995b255`
  revision 1 and run `853db026-e489-46b4-92d0-2c2c8fe052ca`; run revision
  is not exposed by CLI. This run inspected PM's metadata, not the full run.
- **Unresolved inside project — TES-71**, UUID `11b1b30a-1d7f-45b7-b7a2-ab33b9000c9c`:
  retained, revision 2, low confidence. S6 cites thread
  `662b104c-5e28-4703-8574-148f0799a200`, proposal
  `b254645f-7d74-4ad4-916d-7da85f879040` and acceptance
  `3a81b2d1-f98f-4931-a801-3e34f31020e5` (comment revisions 1).
  Proposal acceptance does not identify the target repository.
- **TES-85/88 blockers and TES-102 pause**: attributed to S2's project report.
  It cites TES-88 comment `01a0a2d9-74dd-7580-a418-1f5f9574bfab` and TES-102
  user comment `01a0a2e1-4be2-7bc0-a120-69fe5376876f` plus lead confirmation
  `01a0a2e2-7ff9-77ec-ac08-6424cf99912f`. These were not freshly expanded here;
  no comment revision is invented. Check them live before operational action.

Earlier first-pass documents and validation remain in TES-103 delivery comment
`01a0a2ef-f0dc-7f36-9ccb-b558cc6cbd03`, revision 1. The local authorization in
S4 supersedes that delivery's pending-audience blocker, not its recorded history.


## Corrected PM evidence and review response

S6 attachment `01a0a305-2bf7-7456-869a-d2d5e6159d55` is the complete PM
correction ZIP; corrected CSV attachment is `01a0a305-2bfe-7840-9a35-dae88ae4e7d4`.
The ZIP was downloaded through authenticated CLI and its manifest verified before
import. Necessary evidence is retained locally without raw comment bodies:

- [Two correction logs](evidence/current-run-changes.json): original exact commands,
  exit codes, before/after values and offset-bearing times. Historical only.
- [37 comment source records](evidence/source-comment-metadata.json): comment UUID,
  revision, published time and PM collection time. These are PM's reads, not new
  independent reads by this integrator.
- [Run source metadata](evidence/source-run-metadata.json): TES-93 lifecycle
  classification evidence with explicit acceptance limits.
- [Package provenance and file hashes](evidence/provenance.json): attachment identity,
  package SHA-256 and hashes of those exact copied metadata files.

S8 audit attachment `01a0a30a-8f45-7115-8f21-44e28fdc9a07` verified the previous
package from delivery `01a0a2f9-2eac-79be-a05d-83115c651bc8`.
That previous ZIP `01a0a2f9-2e83-7cc9-bd91-5a8200af8718` remains the fixed
historical artifact. Its nine file hashes matched the local pre-edit files in
this run. It does not certify the newly synchronized files.

All five requested follow-ups are implemented for recheck: moved rows and confidence;
84/80/3/1 and 1-inside/14-outside/15-total counts; specific PM/review and mutation
sources with original historical gaps; offset recovery from JSON logs; refreshed
package/patch/hash validation. The integrator's validation does not replace the
independent recheck coordinated by the lead. See [Status](status.md) for the delta
and observed TES-103 revision drift.
