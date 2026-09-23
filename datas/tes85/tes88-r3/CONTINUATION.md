TES-85 continuation evidence and coordination items

No live template, instance, trigger, run or database mutation was performed.
apply_allowed=false. TES-85 remains in_progress. TES-89 was not repeated.

Identity provenance:
All eight original instance IDs match explicit creation receipts, not input
similarity. Source receipt SHA256 and expected canonical key are recorded per ID.
The original ca96a34 sync implementation already submitted
buildcatalog:v1:SHA256(project_key + NUL + target_key). Historical replay receipts
show the same IDs. This is evidence of creator intent and historical idempotency;
it is not a current read of the stored idempotency_key.

Live change since the old 8/8 report:
All eight are now archived (2026-09-14 06:55:52 through 06:56:25 -07:00),
with revisions advanced. They are historical confirmed targets, not eight active
instances. The CLI response still omits managed_source/external_key. Preserve
archival, current revision and user inputs; never auto-adopt by matching input.

The listener on port 8081 was PID 71264, binary server/bin/script-pipeline/server.exe.
Its SHA256 is fe643ae45324a3f5c00f1882417429d80a2ee68df3d23b03f6d0747b6b934a1e;
Go build metadata is vcs.revision=238a52b3645249e35b50ccef74dbdf4f6f37a564 and
vcs.modified=true. This is not proof of reviewed 79afa3cfd deployment.
The reviewed handler derives managed fields from stored buildcatalog:v1 keys;
the current checkout lacks that projection. This is consistent with a version
mismatch, but the modified binary and unavailable raw key prevent a definitive
claim that no data migration is needed.

Migration / rollback contract:
1. Keep apply_allowed=false until a matched read-capable service and explicit
   archival policy are established. No unarchive is included.
2. Read each original ID plus workspace, template, revision, archived_at and raw
   idempotency_key through an approved platform operation. Current CLI responses
   do not expose the raw key; do not bypass this by querying the live database.
3. If the existing key equals the proven canonical key, data migration is a no-op:
   restore the API projection in the approved service candidate only.
4. For a missing/different key, stop unless original request/storage evidence and
   an explicitly approved per-ID old-key -> new-key plan exist. In an isolated
   restored database, lock exact workspace/ID rows; compare old key, revision and
   archive state; reject any competing canonical key; update atomically, retain
   all user fields and archival, record before/after rows and one CAS revision
   change. A retry seeing exact recorded after-state is a no-op.
5. Rollback must CAS against that recorded after-state. Any subsequent edit,
   key reuse or revision change blocks rollback; never force overwrite. Restore
   key ownership while keeping revisions monotonic. Production remains gated.
No raw-key database migration or database rollback was run in this turn.

Isolated protocol check:
A fixture synthesized from observed IDs/revisions/archive states predicts eight
creates when managed projection is absent. Supplying only the proven candidate
projection produces eight archived rows on both reconciliations, with identical
IDs/revisions and no write calls. Removing the simulated projection exactly
restores the fixture. This is in-memory protocol evidence, not DB migration or
live-service integration evidence.

P0 integration:
Created an isolated candidate at fixed upstream
9d18186e6e8cfa168d6053d33d652e86fadfc12b, using e44b6a8d7 source selections.
Extracted only the selected P0 migration sources and workflow query for a
generation probe. sqlc generate fails:
pkg/db/queries/workflow.sql:149:5: column "request_hash" does not exist.

Concrete dependency decision:
workflow_run.request_hash comes from 273_workflow_external_handoff.up.sql,
outside the 162-file selection. That migration ALSO introduces callback
destination/delivery tables and an encrypted signing-secret column. Copying the
whole migration would silently expand callback scope, so it was not imported.
Proposed smallest expansion: a new additive, complete-filename migration adding
only workflow_run.request_hash TEXT (idempotent IF NOT EXISTS), plus a guarded
down migration; remove callback_destination_id from the minimal published query/
engine contract. Keep instance snapshot fields for the authorized P2/496 stage.
Review the minimal added-column diff before proceeding beyond this boundary.

The old 232/235/244 sources also contain seven inline primary keys. Their implicit
index creation needs conversion to separate concurrent unique-index builds and
subsequent primary-key attachments under current repository rules; do not simply
ship the archived migration bytes. This remains unimplemented in the probe.

P0 has not passed generation/build/database gates, and no P1-P3/full integrated
candidate is claimed. No upstream file was replaced in the main working tree.
The scratch candidate is unpublished and cannot replace the current full platform.
Native Mac/Linux acceptance, current-service compatibility, raw-key evidence and
the minimal dependency decision remain open. No remote PR/release was created.
