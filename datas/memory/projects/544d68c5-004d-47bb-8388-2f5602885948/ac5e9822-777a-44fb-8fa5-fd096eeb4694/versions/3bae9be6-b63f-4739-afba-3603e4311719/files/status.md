# Project status

- id: multica-project-status
- topic: Corrected membership snapshot and remaining classification questions
- status: verified
- verified_at: 2026-09-14T20:11:40-07:00
- verified_commit: not applicable
- owner_role: PM supplies classification; implementation engineer integrates rows and counts
- sources: [Source register](sources.md), [project rows](issues.csv), [outside-project candidates](classification-pending.csv), [correction evidence](evidence/current-run-changes.json)
- conclusion: Live CLI reads match PM's corrected 84-task project set. PM rates
  83 tasks as sufficiently supported and leaves one in-project classification open.
  This verifies the snapshot, not feature delivery or final independent acceptance.

## Scope and collection

Project UUID: `ac5e9822-777a-44fb-8fa5-fd096eeb4694`; workspace UUID:
`544d68c5-004d-47bb-8388-2f5602885948`.
Collection interval: **2026-09-14T20:11:40-07:00 — 2026-09-14T20:11:40-07:00**.
Workspace: **98 unique tasks**. Project: **84 unique tasks**.
Both inventories reached `has_more=false`; additional reads at offsets 98 and 84
returned empty pages. Project-filtered IDs equal the workspace project subset and
the live project summary. Sequential reads are not an atomic transaction.

PM correction snapshot: 2026-09-14T19:58:39.7605536-07:00 through
2026-09-14T19:58:40.2766472-07:00. No differences in UUID membership,
project, status/category, assignee or parent fields.
Revision drift against PM: **TES-103 32 -> 39**.
TES-103 has subsequent comments; source revisions in classification references
remain PM's observed revisions, while `revision` is this collection's live value.
Further comments, including this delivery, can advance it again.

## Counts derived from the same rows

| Status category | Count |
| --- | ---: |
| done | 80 |
| in_progress | 3 |
| backlog | 1 |
| todo | 0 |
| in_review | 0 |
| blocked | 0 |
| cancelled | 0 |

| PM theme | Count |
| --- | ---: |
| 工作流与平台能力 | 33 |
| 文档审计 | 4 |
| 配置与性能 | 2 |
| Agent 接入与运行时验收 | 26 |
| 协作与质量治理 | 5 |
| 工作流实例、界面与 MCP | 6 |
| 跨平台构建工作流 | 6 |
| 多机器协作 | 1 |
| 项目整理与 memory | 1 |

## Classification placement and confidence

- **84 in project = 83 supported by PM + 1 in-project pending (TES-71).**
- **14 outside-project pending**: TES-29, 56, 76, 78, 90–92, 94–100.
- **15 unresolved total = 1 inside + 14 outside.** Do not equate project membership
  with confirmed classification. Confidence concerns relevance, not completion.

[issues.csv](issues.csv) contains all 84 project rows, including TES-71 with
`classification_state=in_project_pending` and `classification_confidence=low`.
[classification-pending.csv](classification-pending.csv) contains only the 14
outside rows, all `outside_project_pending`. There are no duplicate rows across
these files. The PM package's pending CSV has 15 rows because it also includes
TES-71; this memory layout deliberately keeps each UUID in exactly one file.

| Issue | Current placement / confidence | Evidence limit |
| --- | --- | --- |
| TES-77 | In project; medium | Multica CLI/server/workspace troubleshooting supports relevance. Original OS-query purpose remains uncertain; no proof of defect, deletion or repair. |
| TES-93 | In project; high | Run-only lifecycle description, comment and PM run metadata support relevance; no claim of formal TES-85 or full end-to-end acceptance. |
| TES-71 | In project; low, pending | Generic branch/nightly/review proposal lacks target repository or explicit project connection. Keep placement; PM needs purpose/repository evidence. |
| TES-29, 76, 78 | Outside; low, pending | OS-query evidence lacks a project purpose. |
| TES-56 | Outside; low, pending | Audit repository and delivered audit evidence missing. |
| TES-90–92, 94–100 | Outside; low, pending | Script-run evidence does not establish platform purpose; run failure alone does not decide membership. |

## Phase and changes from the previous memory

Previous delivery `01a0a2f9-2eac-79be-a05d-83115c651bc8` had 82 project rows,
16 outside candidates and 78 done. This version moves TES-77 and TES-93 into
the project file: **82 -> 84**, outside **16 -> 14**, done **78 -> 80**.
Agent/runtime theme **25 -> 26**; cross-platform workflow theme **5 -> 6**;
other themes and active/backlog counts are unchanged.
The previous text separately flagged TES-71 but did not aggregate it with outside
candidates; the corrected total of 15 unresolved explicitly includes that one row.

- TES-85/88 remain `in_progress`; repair delivery and recheck boundaries are
  attributed to PM's report, not new product testing here.
- TES-102 remains `backlog` under the cited user pause.
- TES-103 remains `in_progress`. The previous fixed document package passed
  independent review (S8); this synchronized version still needs independent recheck.
- Historical `done` does not prove merge, deployment or native tests.

## Evidence, time recovery and refresh

All rows retain original PM placement claims and missing historical fields, and
now import PM's specific classification source/revision and confidence.
Current live revision/time and PM classification revision/time are separate fields.
The two correction logs preserve null -> project and revision **1 -> 2** (TES-77),
**4 -> 5** (TES-93), with protected fields unchanged.
`correction_before_collected_at` and `correction_after_collected_at` come from
[evidence/current-run-changes.json](evidence/current-run-changes.json), preserving
their original ISO 8601 offsets and fractional seconds. PM CSV's offset-free
`current_run_before_collected_at` is not used. Its offset-free `issue_updated_at`
is also omitted; collection time is not substituted for update time.
For unchanged rows correction fields are `not applicable`, not invented history.

Original 7 existing + 75 reported moves remain attributed to the initial PM report.
The two new logged corrections do not establish the missing historical revisions,
per-row times or mutation logs for those original 75 moves.
Commands in evidence logs are historical records, not instructions to execute.

Use authenticated CLI project/resource reads and fully paginated issue reads.
Refresh rows, revisions, source references and every count together; compare sets
and fields, not only totals. Check live state before action and after seven days.
Retain previous local packages and cite their fixed review; never label the new
version reviewed solely because its predecessor passed. No platform state is
changed from these files.
