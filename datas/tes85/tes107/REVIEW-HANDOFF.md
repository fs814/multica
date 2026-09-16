TES-104 rebind proposal for PM (not a review verdict)

Bind the new review to 4fc4d1ff2e9ab0688de33732f6c682ff6209cb64 and the attached
TES-107-final.bundle / CANDIDATE.json. Do not keep 0ca4c63c as the nominal reviewed
candidate while using R7 results. PM should verify this packet, update TES-104's
description/attachments, then submit it as in_review to an independent reviewer.
TES-107 does not update TES-104 or self-certify P0/P1.

Mapping and review delta

- R5 e6865d16a9a8110c6c06db8fa9b0cfff94437308 is the common ancestor of R6 and
  callback f932e6eea7b82631acf6cf02cbef33e2662e7e28.
- R6 fa698dcfc5f143a5c5c372944dbafb2b2ae2577b adds workspace teardown/fencing;
  R7 38aad73c26fbb2064d32ff4a9631f3e96d474faf adds migration freeze tests.
- Integrated callback 5154e5603 is a cherry-pick, with byte-identical three-file
  diff to the standalone callback commit. No old test file was copied wholesale.
- Final 4fc4d1ff2 adds only the path-suffix test normalization and archive absolute
  path rejection (two files, six insertions/one deletion).
- The old P0 is a different branch, not an ancestor of the final candidate. The
  exact old-P0-to-final diff is 85 files (24654 insertions, 177 deletions), so a
  blanket reuse of the old whole-service verdict would be invalid. Full file list,
  numstat and patch are supplied; final-to-R7 is only five files.

R1/R2/down: both server/migrations and server/cmd/migrate Git trees are byte-for-byte
identical across old P0, R5, R6, R7 and final. No SQL, ledger name, hook, condition,
runner or rollback guard has changed. The final candidate's ten TestTES85 cases
pass on a fresh dedicated tes85_p0_* database (see migrations.log); these exercise
ownership preservation/recovery and literal-prefix/down refusal. The old independent
TES-104 R1/R2 evidence (comment 01a0aa16-e9b3-7284-95c1-43913c49384a) remains
historical evidence for these unchanged components. This run does not independently
repeat its old-application HTTP/backup/restore matrix or sqlc regeneration.

R3 freeze: final internal/migrations tree equals R7. TES-106 comment
01a0aa40-f0e3-7c19-890d-55135d342fd1 independently accepted the exact historical
25-group/63-stem freeze and three upgrade/rerun cases. CANDIDATE.json proves the
unchanged trees; the new run repeats the freeze regression and complete migrations
package on a real disposable database. The copied R7 three-scenario database report
is explicitly provenance, not a new TES-107 execution. No DDL renumbering or ledger
rewrite is introduced. Review may reuse that scoped evidence based on identical
objects, and should still confirm bundle/tree hashes itself.

P0/service increment: independently review the R5 permission/intake/cancellation
changes, R6 workspace transaction/fences, integrated callback rejection and the
portable archive fix on this SHA. The complete handler pass and in-process R5/R6
recovery/lock tests are new engineering evidence; they are not independent acceptance
and do not prove native daemon process-tree cancellation.

Remaining gates are explicit in REPORT.md: full-suite failures/timeout, race link
failure, external native chain, Mac/Linux, real eight identities/history snapshots,
full cutover/rollback and production permission. P2/P3 and TES-85 product commits
must not advance on the strength of this packet alone. apply_allowed=false.

Live callback review update: TES-105 reached done during this run. Independent
comment 01a0aa58-2236-76eb-9536-7aaf91ce0ec4 accepted ONLY f932e6ee's callback/P0
scope (674 author subcases plus 1091 reviewer subcases). The byte-identical patch
mapping supports provenance reuse; the new integrated final SHA still needs its
own independent review. The original R5 rejection remains historically valid.