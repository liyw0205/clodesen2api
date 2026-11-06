# clodesen2api

OpenAI-compatible Go gateway with managed client keys, shared request admission and quotas, model policies, account routing, an embedded administration panel, persistent jobs, request audit, and backup/restore.

## Download

GitHub Actions tests and packages main commits. Pushing a version tag matching `VERSION` publishes an immutable release for the following targets:

| Package | Runtime |
| --- | --- |
| `clodesen2api-linux-amd64.zip` | Linux x86-64 |
| `clodesen2api-linux-arm64.zip` | Linux AArch64/ARM64 with the Linux loader |
| `clodesen2api-linux-armv7.zip` | Linux 32-bit ARMv7 |
| `clodesen2api-android-arm64-termux.zip` | Android ARM64, including Termux; uses the Android linker |
| `clodesen2api-windows-amd64.zip` | Windows x86-64; includes `.bat` start/stop/restart scripts |

`linux-arm64` and `android-arm64-termux` are separate builds. A Linux ARM64 binary is not interchangeable with the Android/Termux build because their dynamic loaders differ.

## Quick Start

1. Start the service; if `config.toml` is missing, a safe default file is generated automatically:
   - Linux: `./start.sh`
   - Windows: `start.bat`
   - Termux: `./start.sh` from the Android/Termux package.
2. Open `http://127.0.0.1:6687/`. The root path redirects to the administration panel.
3. On first access, set a 6-20 character panel token.

Add upstream accounts and adjust runtime settings from the panel. The generated configuration file keeps credentials on disk but never exposes them in the UI.

## Client API keys

`/v1/*` is protected by default. The administration session cookie is only for `/admin/*` and cannot call the inference API. Create one independent key per client from an authenticated administration session:

```sh
curl -b admin-cookie -X POST http://127.0.0.1:6687/admin/api/keys \
  -H 'Content-Type: application/json' \
  -d '{"name":"local-client","rpm":60,"models":["gpt-4.1"]}'
```

The response contains `secret` once. Production stores only its SHA-256 hash in `data/control.sqlite3`; legacy `data/api-keys.json` is imported once and preserved. Use the secret as `Authorization: Bearer <secret>` or `X-API-Key: <secret>`.

Managed keys support enable/disable, expiration (`expiresAt`), CIDR allowlists (`ipAllow`), per-key sliding-window RPM (`rpm`), and exact model allowlists (`models`). Updates and rotations require the current `version`; rotation invalidates the previous secret. The management endpoints are `GET/POST /admin/api/keys`, `PUT/DELETE /admin/api/keys/{id}`, and `POST /admin/api/keys/{id}/rotate`.

`limits` supports `maxConcurrent`, `dailyRequests`, `monthlyRequests`, `dailyTokens`, `monthlyTokens`, `dailyCredits`, and `monthlyCredits`; zero means no limit. Request quotas count admitted inference requests, including upstream failures, once per client request. `/v1/models` requires authentication but does not consume inference RPM or request quotas. Counters and leases are shared by instances using the same local database; they do not provide distributed multi-host coordination.

Hard token/credit limits require an operator-verified model usage contract and conservative per-request upper bounds. The supported contract reads integer `usage.total_tokens` and `usage.credits` from JSON or a final SSE usage event. Metered calls make one upstream attempt. Missing or interrupted usage retains its reservation until verified reconciliation in settings; models without a verified contract reject these limited calls. Model prices and concurrent account balance changes are not authoritative usage.

The optional `[api].key` is a deployment-level compatibility key without per-client limits. Keep it empty for normal operation. `[api].allow_no_auth` is an explicit development escape hatch; it only permits anonymous inference when no static or managed key exists and should remain `false` in production. When running behind a reverse proxy, set `[api].trusted_proxies` to the proxy IP/CIDR before using `ipAllow`; untrusted `X-Forwarded-For` headers are ignored.

The panel can schedule one redemption code per day. The schedule uses `Asia/Shanghai` (UTC+8); the code is write-only in the panel and is never included in the configuration API response.

Runtime state, credentials, tokens, logs, and local credit history belong in `data/` and are intentionally excluded from source packages and Git.

## Control Data And Recovery

Production uses SQLite for keys, accounts, administration sessions, settings overrides, model policies/cooldowns, admission, jobs, audit, credit history and cache. Legacy files are imported once per repository and retained; production stops writing those JSON repositories. The database path follows the directory of `admin.token_file`.

Settings report their source, revision and effective value. Precedence is CLI > `CLODESEN2API_SECTION_KEY` environment > administration overrides > TOML > defaults. Account routing/concurrency and scheduling settings apply immediately; connection, listener and API configuration changes require restart. Conflicting writes return 409.

Settings can create/download a full backup, validate an uploaded archive, and request restoration. Backups contain credentials and administration tokens. They use a WAL-consistent database snapshot, a file manifest and hashes; restoration validates schema and control data, excludes other instances, drains work and restarts before replacing data. Replacement failures roll back original files. Startup failures after successful replacement require operator recovery from the pre-restore backup; this is not a host or binary backup. Unix restart restoration is exercised end to end; Windows is cross-built and its replacement-process restart still needs runtime validation.

Batch checkin, redemption, refresh and connection probes run as bounded persistent jobs. Page reloads keep their progress. Uncertain redemption results are not automatically retried. A connection probe checks login and model listing rather than paid inference.

## Development

```sh
go test ./...
go vet ./...
go build ./cmd/clodesen2api
npm ci
npx playwright-core install chromium
npm run test:web
```

`make build` reads `VERSION` and embeds the release version. CI also tests Go 1.23, race detection on Linux, browser flows and all five build targets. Design and implementation details are in [the Chinese reference plan](docs/codebuddy2api-reference-plan.zh-CN.md) and [the delivery checklist](docs/control-plane-implementation.md).

The administration panel caches upstream account, model, and credit data for one hour. Refresh forces a shared upstream read; partial failures retain the failed account's previous credit snapshot. Request audit retains up to 50,000 records for seven days with separate cumulative totals and bounded asynchronous writes. The panel reports dropped audit events and storage failures. Account transactions calibrate local credit estimates independently of hard per-key quotas.
