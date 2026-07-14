# autofetch client 1.0.3

Version 1.0.3 adds consistent build metadata to support diagnostics and server-side update detection.

## Highlights

- Client metadata is sent with every authenticated lease poll.
- Startup logs show version, commit, build date, platform, architecture and variant.
- Both headless and GUI binaries support `--version` before configuration or GUI initialization.
- GUI clients show the version in the tray menu.
- Release builds embed metadata for every target and validate binary format, Go metadata, version output where executable, and SHA-256 checksums.

The IRC/XDCC transfer, G-Line handling, channel prerequisite and cancellation fixes introduced in 1.0.2 remain unchanged.
