# Changelog

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
