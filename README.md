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

Hard token/credit limits require an operator-verified model usage contract and conservative per-request upper bounds. The supported contract reads integer `usage.total_tokens` and `usage.credits` from JSON or a final SSE usage event. Metered calls allow one potentially billable upstream attempt; explicit insufficient-credit rejections can select another account. Missing or interrupted usage retains its reservation until verified reconciliation in settings; models without a verified contract reject these limited calls. Model prices and concurrent account balance changes are not authoritative usage.

The optional `[api].key` is a deployment-level compatibility key without per-client limits. Keep it empty for normal operation. `[api].allow_no_auth` is an explicit development escape hatch; it only permits anonymous inference when no static or managed key exists and should remain `false` in production. When running behind a reverse proxy, set `[api].trusted_proxies` to the proxy IP/CIDR before using `ipAllow`; untrusted `X-Forwarded-For` headers are ignored.

The panel can schedule one redemption code per day. The schedule uses `Asia/Shanghai` (UTC+8); the code is write-only in the panel and is never included in the configuration API response. Automatic checkin and redemption each record their daily attempt before execution, regardless of success. Restarts and code/target edits do not repeat it. Explicitly saving a different time still in the future today schedules another attempt at that time; startup never executes a future schedule early.

Image generation and editing switch accounts after an explicit insufficient-credit rejection, including rejection during compute-route allocation or the initial SSE error. Each candidate is visited at most once per client request. Balance at or below 100 enters a 30-minute credit cooldown; balances above 100 can still be insufficient for a particular request and select another account without that cooldown. Expired credit cooldowns stay unavailable until a fresh balance exceeds 100. A failed balance check renews the cooldown. Checks run in the backend every 30 seconds and before account selection; checkin and redemption also update cooldowns from confirmed balances.

Runtime state, credentials, tokens, logs, and local credit history belong in `data/` and are intentionally excluded from source packages and Git.

## Control Data And Recovery

Production uses SQLite for keys, accounts, administration sessions, settings overrides, model policies/cooldowns, admission, jobs, audit, credit history and cache. Legacy files are imported once per repository and retained; production stops writing those JSON repositories. The database path follows the directory of `admin.token_file`.

Settings report their source, revision and effective value. Precedence is CLI > `CLODESEN2API_SECTION_KEY` environment > administration overrides > TOML > defaults. Account routing/concurrency and scheduling settings apply immediately; connection, listener and API configuration changes require restart. Conflicting writes return 409.

Settings can create/download a full backup, validate an uploaded archive, and request restoration. Backups contain credentials and administration tokens. They use a WAL-consistent database snapshot, a file manifest and hashes; restoration validates schema and control data, excludes other instances, drains work and restarts before replacing data. Replacement failures roll back original files. Startup failures after successful replacement require operator recovery from the pre-restore backup; this is not a host or binary backup. Unix restart restoration is exercised end to end; Windows is cross-built and its replacement-process restart still needs runtime validation.

Batch checkin, redemption, refresh and connection probes run as bounded persistent jobs. The task center restores progress across page reloads, shows each account result, and supports cancellation and safe retries. Uncertain redemption results are not automatically retried. A connection probe checks login and model listing rather than paid inference.

The panel includes a persistent desktop sidebar and a mobile navigation drawer, account search/filter/sort and batch enable/disable, URL-restored navigation, one-time key connection templates, request diagnostics, grouped settings with restart impact, runtime health, and persistent login backoff. `/readyz` checks local readiness; `/healthz` checks HTTP liveness. Routing supports fixed, round-robin and random selection with the existing retry and metering limits. Routing and update-source selectors use floating panels that stay inside the viewport.

Time reports support today, 7 days and 30 days, grouping by date/model/account/key, P95 and CSV export. Compact metrics retain 31 days independently of the seven-day detail limit; the panel marks incomplete history before the upgrade rather than presenting old detail as continuous metrics. Credit estimates retain their separate seven-day scope and cannot be attributed to keys. The call playground uses a supplied client key for text, SSE and image requests, requires consumption consent, and clears its key and output on close/logout.

Daily backups and alerts are disabled by default. Backup schedules have retention and failure feedback; alerts persist transitions, recovery and delivery retries. Notifications support 13 independently configured channels: Webhook, Bark, ServerChan, PushPlus, Telegram, WeCom, DingTalk, Feishu, SMTP, ntfy, WxPusher, Gotify and PushDeer. Configure a receiver explicitly before enabling its channel. The full management contract and data coverage are documented in [管理 API](docs/admin-api.zh-CN.md).

## Development

```sh
go test ./...
go vet ./...
go build ./cmd/clodesen2api
npm ci
npx playwright-core install chromium
npm run test:web
```

`make build` reads `VERSION` and embeds the release version. CI also tests Go 1.23, race detection on Linux, browser flows and all five build targets. Design and implementation details are in [the Chinese reference plan](docs/codebuddy2api-reference-plan.zh-CN.md), [the delivery checklist](docs/control-plane-implementation.md), and [the optimization development plan](docs/optimization-development-plan.zh-CN.md).

Account, model and credit caches have separate hot-reloadable TTLs (30 to 86,400 seconds, default one hour). Refresh forces a shared upstream read; partial failures retain the failed account's previous credit snapshot. Request audit uses indexed SQL filtering and pagination, retaining up to 50,000 records for seven days with separate cumulative totals and bounded asynchronous writes. The panel reports dropped audit events and storage failures. Account transactions calibrate local credit estimates independently of hard per-key quotas.
