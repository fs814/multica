TES-88 R1/R2 engineering delivery

Fixed helper: dbe9b85f8dd8359fd0eb0b93cf8adc0bf94a5ac0.
Integrated documents: 440a1849dd922687d4a26accbb9642b327f96069.
Product: 79afa3cfd14cf0b559324140c881e2ae50ab27e0.
Original Settings/Mac v8 and TES-86/87/89 review baselines are unchanged.

R1: compare the complete server file set with SOURCE.json, without extension
filters. Reject additions, removals, checksum changes, links/reparse points and
unsafe manifest paths before Go invocation or output creation. Build output is
outside the source package. The external ZIP SHA is the manifest trust anchor.
Do not edit the source extraction concurrently with a build.

R2: package-root CLI and lowercase settings template paths; two separate
extraction roots; one canonical operator/journal and two ordinary Mac acceptance
instances. MAC-USER-GUIDE-r3.md is the current integrated procedure. Unmodified PM
revision 2 and its receipt are preserved under provenance/. The source ZIP SHA
was updated; use external SHA256SUMS-r3.txt for the handoff ZIP.

Verification: helper suite 12/12 (13 added-input subcases); actual source ZIP
adds Go, assembly, embed resource and hidden resource: 4/4 refused before output.
Go 1.26.6, CGO disabled, readonly dependencies, offline warm cache: Linux amd64
and arm64 each built from two independent extraction roots with equal per-arch
binary SHA256. See evidence/linux-reproducibility.json.

These are Windows cross builds, not Linux native execution. Mac/Linux native
acceptance and independent TES-88 re-review remain pending. No instance import,
template publication, deployment or production cutover was performed.
apply_allowed=false. No remote PR was created.
