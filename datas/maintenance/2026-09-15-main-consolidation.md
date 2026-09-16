# Main consolidation and final Memory storage

- Final deliverables: `datas/`; intermediate builds, logs, backup bundles and unselected candidates: ignored `.multica/`.
- Multica source-backed Memory reads server-selected snapshots in `datas/memory/projects/`; readable exports accompany each selected generation. Git attributes preserve hash-verified bytes.
- Existing final content (12 entries) was preserved and verified through the live API with the legacy path absent. A content-preserving write verified staging and promotion; content revision is 5.
- Autopilot runbook branch `agent/agent/750e9b5a` was merged, including multiline/file descriptions and current unified skill references.
- TES-66 v2 history was reconciled with canonical main. Its features were already integrated and extended by 584095fc5, 579fa33a2, b6f0de5d7 and 6fd598a58. Commit 19ebf7b95 already preserves 54 superseded SQL files as migration compatibility fixtures; they must not be deployed again.
- All other local and origin feature branches were already ancestors. A local branch bundle was retained under `.multica/` before cleanup. Official upstream branches are outside this repository's branch cleanup.
- Validation: project memory storage and promotion tests; targeted Go Memory/Autopilot/IssuePool tests; 22 core Memory tests on Node 22; 11 Memory view tests; live API read/write against the final storage.
- Existing maintenance no-task-claims mode remains enabled; branch/data consolidation does not change task execution policy.
- Final restart acceptance: detached owner reads revision 5 from final storage; all 26 committed snapshot/export files match local bytes and all 12 live entries match the selected digest.
