# Control plane implementation

This tracks v0.3.0, adapted from codebuddy2api v1.5.4 to Go and embedded static
JavaScript. Source implementation is complete; remote delivery gates below
remain pending until verified. Keep the upstream protocol and account workflows.

Reference comparison, frontend/backend mapping, proposed contracts and acceptance
criteria: [Chinese implementation plan](codebuddy2api-reference-plan.zh-CN.md).

## P0 - API key lifecycle and UI

- [x] Hash-only storage, create/update/disable/expire/delete/rotate, optimistic version.
- [x] Header authentication before buffering, trusted proxy IP/CIDR, model allowlist, RPM.
- [x] Keys navigation, desktop table, mobile cards, create/edit/confirm dialogs.
- [x] One-time secret display and copy, clear on close/logout, no reveal endpoint.
- [x] Browser lifecycle, policy, audit and conflict regression at 1440/768/390/320 widths.
- [x] Visual screenshot review at 1440/768/390/320 widths after layout corrections.
- [x] Expiry/IP/copy failure/logout and late-response browser cases.
- [x] API management authentication, limit clearing, conflicts and lifecycle regression tests.

## P1 - Principal and audit

- [x] Principal in request context, hash index, preauthenticated rotation revocation.
- [x] Key ID/name, resolved client IP, request ID, authorization result/error code.
- [x] Per-key 24-hour summaries and key filter in request records.
- [x] Periodic last-used persistence and final flush.
- [x] Request-start event, bounded asynchronous audit, degraded-storage visibility.
- [x] Atomic audit import, owner heartbeat/recovery checks and shared query snapshots.
- [x] Receipt retention boundary tests and pre-upgrade WAL backup.
- [x] Linux race validation in remote CI (Android local runtime lacks race support).
- [x] End-to-end metadata/denial tests and restart verification.
- [x] Structured safe error codes, parameter allowlist and per-attempt audit metadata/UI.
- [x] Response-write panic is recorded as interrupted/failure, including aggregates.

## P2 - Model policy

- [x] Catalog normalization, global enable/disable, public aliases and capabilities.
- [x] Unified global/key model decision and filtered public model list.
- [x] Model policy API/editor, version conflict handling and mapping validation.
- [x] Persistent account/model 429 cooldown with reason, last error and recovery time.
- [x] Go integration and browser coverage for aliases, disablement, conflicts and cooldown display.
- [x] Full mapping-chain, missing/disabled target, shared-store and partial catalog regressions.

## P3 - Admission and reliability

- [x] Shared global/key/model admission, body budget and capacity Retry-After foundation.
- [x] First-byte/idle/overall timeout, client cancellation and bounded account attempts.
- [x] Atomic daily/monthly request counts, shared rolling RPM and key quota UI.
- [x] Verified token/credit reservations, partial/unknown settlement and reconciliation.
- [x] Concurrent-store quota, token-only/credit-only and partial overrun rejection tests.
- [x] Admission lease heartbeat/fencing and request/body/response budget review.
- [x] JSON/multipart 408 and shared route-retry/relogin attempt-budget regressions.
- [x] Persistent checkin/redemption/refresh/connection-probe jobs and progress/cancel/retry UI.
- [x] Legacy redemption date import and schedule receipts independent of job history.
- [x] Single-flight refresh and backend cache state/age/error endpoint.
- [x] Cache state/partial failures UI and account-specific stale snapshot preservation.
- [x] Concurrent admission, cancellation, stream failure and persistent job reload tests.
- [x] Queue-capacity/restart/cancel/retry Go tests and browser reload/progress flow.
- [x] Schedule receipts prevent same-day replay after changing code/targets.

## P4 - Control persistence and operations

- [x] SQLite document/CAS foundation, API-key JSON import and schema-version checks.
- [x] Repository migration with atomic per-repository import/recovery and original files preserved.
- [x] SQLite keys/accounts/settings overrides/sessions/policies/cooldowns/jobs/audit.
- [x] Configuration source/locks, revision/CAS, candidate validation and hot/restart UI.
- [x] Legacy strategy compatibility, cross-instance and late-session settings review.
- [x] WAL snapshot archive, manifest/hash/schema checks, upload plan and offline restore test.
- [x] Backup creation/download/validation UI and automatic pre-update backup.
- [x] Restore drain/restart/rollback, other-instance exclusion and pre-schema WAL backup.
- [x] Credit/redemption/cache repositories migrated to SQLite.
- [x] Restore schema/config/document semantic validation and rollback fault injection.
- [x] Shared RPM/request admission counters and two-store concurrency tests.
- [x] Shared-instance leases and real two-process Unix restore/restart validation.
- [ ] Remote release matrix verification.

Hard token/credit limits require an operator-verified `upstream-usage-v1` contract
and conservative integer bounds. Defaults reject these limited calls. Unknown
usage stays reserved. The parser expects one final JSON/SSE usage object; it is
not a general billing adapter or financial ledger. Coordination is for processes
sharing a local database, not multiple hosts. Legacy repositories import in
individual transactions, not one transaction spanning all legacy files.

Rollback covers replacement failures. A later startup failure needs operator
recovery from the pre-restore backup. Windows has been cross-built; its
replacement-process restore path has not run on Windows.

## Delivery gates

- [x] Local Go tests, vet and five browser flows; four-width screenshot review.
- [x] Final five-target cross-builds and source diff review.
- [x] Staging/secret review; task files committed and pushed as `a65b1b6`.
- [x] VERSION and release notes; main validates/packages, version tag publishes without clobber.
- [ ] Commit task files, push main/version and verify remote refs/CI.

Remote run [42309324867](https://github.com/liyw0205/clodesen2api/actions/runs/42309324867)
passed Go 1.23, stable tests/vet and Linux race. Its slow browser dependency
installation was cancelled; browser CI now uses the runner's installed Chrome
on a pinned Ubuntu 24.04 image, with a job timeout. Browser/package/release gates
are still being verified. Source packages include the browser manifests and
test fixtures so the documented development checks remain runnable.

The working tree also contains pre-existing unrelated binaries, archives and
browser artifacts. They must not be staged, removed or reverted for this task.
