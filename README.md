# clodesen2api

OpenAI-compatible gateway with multi-account rotation, random selection, 30-minute credit-exhaustion cooldown, an administration panel, model pricing, credit usage history, and scheduled redemption-code credit resets.

## Download

GitHub Actions publishes packages for the following targets:

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
2. Open `http://127.0.0.1:8080/`. The root path redirects to the administration panel.
3. On first access, set a 6-20 character panel token.

Add upstream accounts and adjust runtime settings from the panel. The generated configuration file keeps credentials on disk but never exposes them in the UI.

The panel can schedule one redemption code per day. The schedule uses `Asia/Shanghai` (UTC+8); the code is write-only in the panel and is never included in the configuration API response.

Runtime state, credentials, tokens, logs, and local credit history belong in `data/` and are intentionally excluded from source packages and Git.

## Development

```sh
go test ./...
go vet ./...
go build ./cmd/clodesen2api
```

The administration panel caches upstream account, model, and credit data for 12 hours. Its refresh buttons force a new upstream read. Local successful model requests are recorded immediately and retained for seven days; upstream account transactions periodically calibrate those local estimates.
