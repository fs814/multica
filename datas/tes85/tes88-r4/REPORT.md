TES-88 R2 r4 documentation delivery

Fixed commit: f79f17ceaae0d269967437cc4c2de0496d8e482e
Parent: 440a1849dd922687d4a26accbb9642b327f96069
Helper code remains dbe9b85f8dd8359fd0eb0b93cf8adc0bf94a5ac0.
Product remains 79afa3cfd14cf0b559324140c881e2ae50ab27e0; Mac v8 unchanged.

README commands use -s . from the extracted handoff root. The integrated guide
retains its MAC-USER-GUIDE-r3.md filename, explicitly marked r4. The original
eight canonical instances are archived historical records with changed revisions,
not current active coverage. Preserve archival; never automatically restore,
adopt by input similarity, or reinitialize. Read current per-ID archive/revision/
identity before reconciliation; stop if evidence is missing. apply_allowed=false.
Original PM revision 2 remains byte-for-byte under provenance/.

R1 independent approval from r3 is retained. Both helper scripts, both tests,
the r3 source ZIP, and all inherited evidence/*.log/json are unchanged. Inherited
cross-build evidence is from Windows, not Linux native execution. Current r4
fresh-extraction test results are delivered separately in VERIFICATION.json and
package-root-*.log, tied to this final ZIP SHA256. This ZIP is not repacked after
those tests. R2 independent re-review and Mac/Linux native acceptance are pending.

Review/integration handoff:
- TES-88-r4-docs.patch applies on 440a1849dd922687d4a26accbb9642b327f96069.
- TES-88-all-helpers.patch contains only e2e/delivery additions relative to
  1c3da11ebb70502b0d43fb9d1d56d9c1aab07016, so reviewers can inspect all missing helpers independently.
- TES-88-helper-history-r4.bundle includes initial helpers, R1 repair, r3 docs and
  this r4 commit. Bundle prerequisite: 1c3da11ebb70502b0d43fb9d1d56d9c1aab07016.
  It is incremental, not a standalone repository or proof of main integration.
- No PR: gh auth status reports no logged-in GitHub host; TES-85/88 PR lists empty.
  No main merge, publication, instance import or deployment was performed.
- Parent P0 integration is separately pending: workflow_run.request_hash minimal
  migration, callback-field exclusion, seven primary-key/index splits, followed
  by generation/build/database gates; full P1-P3 candidate is not yet available.
  See TES-85 coordination comment 01a0a843-9366-75cf-a5c4-0e3c7a1dff76.
  Helper review patches do not authorize expanding that scope.

Use external SHA256SUMS-r4.txt for this handoff and the unchanged r3 source ZIP.

Fresh final-ZIP verification: route suite 5/5, full suite 12/12; no skips.
All internal hashes and Git document/helper bytes match. See VERIFICATION.json.

Full helper patch: git apply --check passed on current workspace baseline
d4f0733f0; no helper files were applied there. Fixed-commit diff --check passed.
TES-88-r4-evidence.zip contains both patches, incremental Git bundle, bundle
verification log, package-root command logs, this report and VERIFICATION.json.
