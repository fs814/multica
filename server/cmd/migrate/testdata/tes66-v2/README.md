# TES-66 parallel migration history

These 54 SQL files are preserved byte-for-byte from `c6536b049` for migration
review and compatibility tests. They are **not** part of the production migration
directory. `main` had already deployed the equivalent WorkflowRun migration chain
470–490, with different names/order from this branch.

Running the branch's 459 on an upgraded main database would add existing columns
again; replaying prototype constraints would reject active canonical states.
Main's existing 470 pre-hook and 478 forward migration retain legacy execution
audit information, quarantine unmappable work as blocked, and copy notification
history to the durable outbox. Those deployed identities and semantics remain.

The upgrade tests now use the actual archived 459–469 SQL instead of manually
reconstructing its schema. The branch's invalid inbox-index recovery test targets
main's corresponding migration 479. Dynamic cycle counters are read from the
canonical items, so no parallel aggregate-column migration is needed.

Application merge retains main's stable agent-only error code, bulk review UI,
offset pagination and whole-pool metrics. It adds page pagination, true run totals,
create-batch UI, workflow-required validation, atomic cycle/run terminal updates,
and the branch's missing dispatcher and recovery regression tests. Issue activity
uses `updated_at`, the column defined by this repository's migration history.
