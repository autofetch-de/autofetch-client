# autofetch client 1.1.0

This release introduces separate German and English client artifacts built from one shared codebase.

## Highlights

- German (`de`) and English (`en`) builds for GUI and headless targets
- shared embedded translation catalogs
- localized desktop window, system tray, local browser interface, pairing output, CLI help, settings, and user-facing errors
- stable language-neutral runtime status codes
- technical logs and server-facing error codes remain English and unchanged
- build language included in `--version` and startup diagnostics
- automated catalog parity, usage, fallback, Web UI, and error-localization tests
- English and German client documentation

The selected language is fixed by the downloaded artifact. It is not stored in the client configuration and does not change automatically based on the operating system or account language.
