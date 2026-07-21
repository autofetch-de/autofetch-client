# Client localization architecture

## Release model

The client is built from one source tree into two fixed-language variants:

- `de` for German
- `en` for English

The language is injected through `internal/buildinfo.Language` during the build. It is not stored in `client.json`, inferred from the operating system, or changed by the connected server account.

This intentionally keeps the first bilingual release simple while retaining the foundation for a later runtime language selector.

## Translation catalogs

User-facing text is stored in embedded JSON catalogs:

```text
internal/localization/locales/de.json
internal/localization/locales/en.json
```

The catalogs are embedded into every binary. No translation service, remote request, cookie, analytics component, or additional runtime file is required.

Translation keys describe meaning rather than individual widgets, for example:

```text
action.settings
pairing.valid_until
status.connected
error.server_unreachable
```

English is the technical fallback if an unsupported build value is supplied. A missing key is logged but is never displayed verbatim to the user.

## Status and error separation

Core packages store stable status codes such as:

```text
pairing_waiting
download_running
download_completed
```

GUI, system tray, local Web UI, and CLI output translate those codes at the presentation boundary. Server statuses, API paths, payload fields, IRC commands, protocol terms, and technical error codes remain unchanged.

Technical logs remain in English. Known user-facing errors are converted to a natural explanation by the localization layer while the original technical cause remains available in the log.

## Build metadata

`--version` and startup logs include the fixed language:

```text
language de
```

or:

```text
language en
```

This is diagnostic metadata and is not a runtime preference.

## Adding or changing text

1. Add the same semantic key to `de.json` and `en.json`.
2. Use `localizer.T("key")` or `localizer.T("key", data)` at the UI boundary.
3. Do not store the resulting sentence in shared runtime state.
4. Keep technical codes, routes, paths, commands, and field names untranslated.
5. Run `go test ./internal/localization`.

The tests fail when catalog key sets differ, a key used by the source is missing, or a catalog key is no longer used.

## Future runtime language selection

A later universal client can reuse the existing catalogs and neutral state model. The additional work would be limited mainly to:

- selecting and validating `de` or `en` at runtime
- persisting the local preference
- rebuilding GUI and tray labels after a change
- reloading the local Web UI

No core download or API redesign should be required.
