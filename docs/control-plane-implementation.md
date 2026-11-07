# Control plane implementation

This tracks v0.3.1, adapted from codebuddy2api v1.5.4 to Go and embedded static
JavaScript. Core implementation and release gates below have passed. Keep the
upstream protocol and account workflows; runtime limits remain documented below.

Reference comparison, frontend/backend mapping, proposed contracts and acceptance
criteria: [Chinese implementation plan](codebuddy2api-reference-plan.zh-CN.md).

## v0.3.4 Extension

The optimization work after this release adds resilient resource loading, account
filters/batch actions, saved navigation, connection templates, a task center,
request diagnosis, login protection, readiness/runtime status, indexed audit
queries, grouped settings, per-resource cache TTLs and scheduled backups. It also
adds 31-day compact reports with upgrade coverage, 13 default-off notification
channels, responsive sidebar navigation, floating routing/update-source selectors
and a client-key call playground. Routing retains fixed, round-robin and random
selection; retired stored modes fall back to fixed. Earlier release
receipts below describe that release, not the current task-center behavior.

Scope and validation: [optimization plan](optimization-development-plan.zh-CN.md).
Management responses and coverage: [管理 API](admin-api.zh-CN.md).

The management router now delegates model catalog, credit ledger/statistics,
account snapshots, settings, admission and redemption to separate files. The
frontend separates account actions, request records, analytics presentation and
settings from session/navigation wiring. Conflict recovery preserves local edits;
audit dialogs clear on close/logout. Current workspace validation passes Go tests,
vet and 22 browser flows; this does not change the release receipts below.

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
- [x] Persistent checkin/redemption/refresh/connection-probe jobs; manual account actions refresh data on completion without a task panel.
- [x] Legacy redemption date import and schedule receipts independent of job history.
- [x] Single-flight refresh and backend cache state/age/error endpoint.
- [x] Cache state/partial failures UI and account-specific stale snapshot preservation.
- [x] Concurrent admission, cancellation, stream failure and persistent job reload tests.
- [x] Queue-capacity/restart/cancel/retry Go tests and browser reload/background-action flow.
- [x] Schedule receipts prevent same-day replay after changing code/targets.
- [x] Explicit future-time edits rearm today's schedule; startup does not execute future checkins early.
- [x] Insufficient-credit image failover and expired-cooldown balance verification above 100.

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
- [x] Remote five-platform release matrix verification.

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
- [x] Commit task files, push main/version and verify remote refs/CI.

Release [v0.3.0](https://github.com/liyw0205/clodesen2api/releases/tag/v0.3.0)
targets `5c018b813cc987acb4fcac9b8402df789bcf91c8`; this patch is prepared as
v0.3.1. Both
[main CI](https://github.com/liyw0205/clodesen2api/actions/runs/42310908071) and
[tag CI](https://github.com/liyw0205/clodesen2api/actions/runs/42312031459)
passed Go 1.23, stable tests/vet, Linux race, five browser flows and five-platform
packaging; tag publication also succeeded. The downloaded Termux artifact passed
SHA-256 validation. Source packages include browser manifests and test fixtures.

The initial run was cancelled during slow apt/font installation. Browser CI now
uses installed Chrome on pinned Ubuntu 24.04 with a timeout; the replacement run
passed. The release tag remains immutable; this checklist is a delivery receipt.

The working tree also contains local memory, unrelated binaries, archives and
browser artifacts. `.ai-memory/` and these generated artifacts are ignored and
must not be staged or published.
