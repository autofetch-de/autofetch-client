# Changelog

## 1.2.3 - 2026-11-03

### Fixed

- Fix HTTP downloads failing when the file size reported by the server differs from the size estimated in the download job (e.g. MediathekView sources).
- Recover fully downloaded HTTP partial files after an HTTP 416 response, provided the remote file size and version can be safely verified.
- Preserve strict validation of incomplete or changed HTTP downloads to prevent corrupted files from being marked as completed.
- Fix misleading download speed and ETA reporting after all file bytes have been received.

### Improved

- Show a clear "Finalizing and verifying file" status in the desktop GUI while completed downloads undergo final processing.
- Suppress artificial speed decay during file synchronization and verification, including in progress heartbeats sent to the server.

## 1.2.2 - 2026-11-03

### Added

- Desktop NickServ enrollment: explicit registration, email, confirmation, verification, retry with saved credentials, reset, and sanitized live IRC progress.
- Display server-provided IRC channels read-only in desktop settings; jobs continue to supply their required channels.

### Changed

- Store NickServ registration email separately for each IRC network in local `irc-secrets.json`; prefill it in both desktop and WebUI registration forms. Never reuse the legacy global registration email for new enrollments.
- Limit routine IRC NOTICE, bot PRIVMSG, channel-join and XDCC request logs to `AUTOFETCH_IRC_DEBUG=1`; keep failures visible by default.
- Remove the obsolete startup message about ignored legacy local archive flags.

## Unreleased (v1.2.1)

### Added

- Unified Web UI settings form: optional password setup and change, single Save action at the bottom, prominent login username (`autofetch`), immediate HTTP Basic Auth enforcement and protected local storage.
- A configured password takes precedence over a lingering `--webui-lan-no-password` startup flag, preventing silent re-exposure after a restart.
- Opt-in access to the local Web UI from a single explicitly configured private LAN IP, protected by a password file (HTTP Basic Auth) or an explicitly enabled no-password mode.
- `--webui-password-file`, `--webui-lan-no-password`, and equivalent `AUTOFETCH_WEBUI_*` environment variables; settings can also be stored in `client.json` without storing the password itself.
- Explicit `--headless --webui=true` or `AUTOFETCH_WEBUI=true` allows local Web UI operation on headless / systemd installations. Plain `--headless` still disables the Web UI.

### Fixed

- Publish successfully completed DCC, XDCC, HTTP and extracted files with mode `0644` while keeping incomplete `.part`, metadata, and private archive markers at mode `0600` on Unix.
- Extend Beta support for classic RAR multipart sets (`.rar`, `.r00`, `.r01` etc.), including contiguous-part checks, full decoder verification and nested extraction safety.
- Skip the duplicate-case filename fixture on case-insensitive filesystems such as common macOS installations, where separate filenames cannot coexist.
- Return local Web UI bind errors immediately, refuse wildcard/public-interface bindings and reject unauthorized LAN requests including cross-site state changes.
- Carry forward the public v1.2.0 GitHub ARM/QEMU build verification and MIT copyright information.

### Preserved

- DCC RESUME/ACCEPT, including existing partial-transfer safety tests.
- Stable published v1.2.0 release/tag and server API compatibility.

## 1.2.0 - 2026-10-30

### Added

- Support filename-based IRC download commands in addition to numbered XDCC packs.
- Resume interrupted DCC downloads with RESUME/ACCEPT handling and safety checks.
- Optional local ZIP, TAR, RAR and 7z extraction with configurable layout, disk-space checks, progress and explicit retry.
- Local download and archive history in the Web UI, plus an activity view in the desktop client.
- Manual client update notifications for stable releases; no automatic installation.

### Changed

- Improve IRC offer waiting, download handling and controlled client shutdown.
- Pause server polling after repeated invalid client credentials, until re-pairing.
- Document the opt-in legacy-CN TLS compatibility behavior for trusted certificates.

## 1.1.0 - 2026-07-21

### Added

- Add shared German and English translation catalogs for GUI, system tray, local Web UI and headless pairing output.
- Build separate `de` and `en` release artifacts from the same source tree.
- Include the fixed build language in startup and `--version` metadata.
- Add catalog parity, fallback and user-error localization tests.
- Add German client documentation alongside the English README.

### Changed

- Replace localized runtime status strings with stable language-neutral status codes.
- Keep technical logs and server-facing error codes in English while localizing user-facing explanations.
- Replace German Reverse DCC errors in the download layer with stable technical codes and structured parameters.
- Update GitHub Actions and local build scripts to produce both language variants.

## 1.0.2 - 2026-07-14

### Fixed

- Detect incomplete XDCC transfers by comparing received and expected file sizes.
- Preserve partial downloads and only move completed transfers to their final filename.
- Avoid rapid IRC reconnect loops within a single job attempt.
- Wait for IRC registration and required channel joins before requesting a pack.
- Handle additional prerequisite channels required by XDCC providers.
- Improve IRC error reporting for `NOTICE`, relevant `PRIVMSG`, `ERROR`, connection closures, and join failures.
- Detect IRC G-Lines, sanitize their messages, and avoid immediate automatic retries.
- Improve cancellation of running manual downloads.

### Added

- Configurable IRC networks, nicknames, NickServ, and SASL authentication.
- Tests for incomplete XDCC transfers, ACK handling, and G-Line detection.
- MIT license for the public client source code.

## 1.0.3 - 2026-07-14

- Report client version, platform, architecture, variant, commit and build date during lease polling.
- Log complete build information at startup.
- Add `--version` support to headless and GUI binaries without starting the GUI.
- Show the installed version discreetly in the GUI tray menu.
- Embed and verify release metadata for all GitHub Actions build targets.
- Preserve the IRC/XDCC reliability and cancellation fixes from 1.0.2.