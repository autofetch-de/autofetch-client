# autofetch client

The autofetch client runs downloads on your own device. It pairs with an autofetch account, receives download jobs, stores files in a local download folder and reports the technical result to the server.

The repository provides a graphical client with a system tray, a local browser interface and a headless command-line build.

[Deutsche Dokumentation](README.de.md)

## Language builds

Every release contains two fixed-language variants built from the same source code and translation catalogs:

- `de`: German user interface
- `en`: English user interface

Choose the language suffix when downloading a release, for example:

```text
autofetch-gui-windows-amd64-en-v1.1.0.exe
autofetch-headless-linux-arm64-de-v1.1.0
```

The language does not affect API paths, configuration keys, technical error codes or log terminology. Technical logs remain in English so support information stays consistent across both builds.

## Available variants

### Graphical client

The graphical build includes:

- desktop status window
- system tray menu
- pairing flow
- download progress
- general settings
- local IRC, NickServ, SASL and Reverse DCC settings

Graphical release targets currently cover:

- Windows AMD64
- macOS Intel
- macOS Apple Silicon
- Linux AMD64

### Headless client

The headless build is intended for servers, NAS systems and low-power devices. Release targets currently cover:

- Linux AMD64
- Linux ARM64
- Linux ARMv7
- Windows AMD64
- Windows 386
- macOS Intel
- macOS Apple Silicon

The normal command-line client can also expose a status interface on `127.0.0.1:23324`. Use `--headless` to disable the local interface.

## First start and pairing

Run the downloaded binary. The filename depends on the selected language and platform:

```bash
./autofetch-en
```

On first start, the client displays a pairing code and the autofetch pairing page. Enter the code in the portal and approve the client. The client then stores its credentials locally and starts processing jobs.

To discard the stored client credentials and start pairing again:

```bash
./autofetch-en --re-pair
```

To print only the pairing code:

```bash
./autofetch-en --print-code-only
```

## Local configuration

The client stores its configuration below the operating system's user configuration directory:

```text
autofetch/client.json
autofetch/irc-secrets.json
```

`client.json` contains the server connection, client credentials, download folder and non-secret IRC settings. IRC passwords and SASL credentials are stored separately in `irc-secrets.json`. Both files are written with restrictive permissions where the operating system supports them.

The build language is not stored in the configuration. It is compiled into the selected `de` or `en` release artifact.

## Linux systemd example

Install the selected headless binary under a stable local filename:

```bash
sudo install -m 0755 autofetch-headless-linux-amd64-en-v1.1.0 /usr/local/bin/autofetch
```

Create `/etc/systemd/system/autofetch.service`:

```ini
[Unit]
Description=autofetch client
After=network-online.target
Wants=network-online.target

[Service]
User=autofetch
Group=autofetch
ExecStart=/usr/local/bin/autofetch --headless
WorkingDirectory=/var/lib/autofetch
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Then enable the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now autofetch
journalctl -u autofetch -f
```

## Privacy and external communication

The client contains no analytics, advertising, tracking or marketing telemetry. It communicates with the configured autofetch server only as required for pairing, configuration, job leasing, heartbeats and result reporting.

When Reverse DCC is enabled, the client may contact public IP detection services to determine the address announced to an IRC bot. Set `AUTOFETCH_DCC_PUBLIC_IP` to provide the public IP explicitly. Technical logs may contain hostnames, channels, filenames and network errors; review logs before sharing them with support.

## Build metadata

Both GUI and headless binaries support:

```bash
./autofetch-en --version
```

The output includes the version, commit, build date, platform, architecture, variant and fixed language.

## Development checks

Run the non-GUI test suite with:

```bash
go test ./internal/localization ./internal/observe ./internal/buildinfo ./internal/api ./internal/config ./internal/app ./internal/webui ./internal/worker ./internal/download
```

A full release build additionally requires the platform-specific Fyne dependencies used by the graphical client.

Architecture and manual verification:

- [`docs/I18N_ARCHITECTURE.md`](docs/I18N_ARCHITECTURE.md)
- [`docs/I18N_MANUAL_TEST.md`](docs/I18N_MANUAL_TEST.md)

## License

The autofetch client is licensed under the MIT License. See `LICENSE` for details.

## Optional archive extraction

Extraction is **disabled by default**. ZIP, TAR, RAR and 7z have now passed local real-world tests (RAR/7z still require `7zz` or `7z` on PATH). Extraction failure does not change a successful download; local extraction history remains available under loopback `GET /api/archives`.

Optional extraction configuration in `client.json`:

```json
{
  "extract_archives": true,
  "extract_layout": "same",
  "extract_delete_original": true,
  "extract_nested_depth": 0
}
```

- `extract_layout`: `separate` (default, `<filename>.extracted/`) or `same` (smart placement alongside the archive). One extracted payload file is placed directly (`Film.rar` → `Film.mkv`); multiple payload files are grouped in a directory derived from the archive filename (`Collection.rar` → `Collection/`). A pre-existing shared top-level directory inside the archive is reused instead of creating a redundant folder. **Existing files/directories are never overwritten.**
- `extract_delete_original`: `false` by default. With `true`, remove originals (including all related multipart volumes) **only after** a complete verified extraction and durable publication checkpoint.
- `extract_nested_depth`: default `0`; depths 1–3 are **BETA**, pending real-world validation.

**Note about flags:** Omitting archive flags keeps values from local `client.json` (unless overridden by environment variables). A previously saved `extract_delete_original: true` remains effective. Use `--extract-archives=false --extract-delete-original=false` to override both for one run. The client logs its effective archive settings at startup.

Equivalent CLI flags: `--extract-archives --extract-layout=same --extract-delete-original`; optional environment variables `AUTOFETCH_EXTRACT_LAYOUT=same`, `AUTOFETCH_EXTRACT_DELETE_ORIGINAL=1`. Download journal and extraction-history JSON files are deliberately retained outside the download folder to avoid unwanted re-downloads on re-leased jobs. The temporary extraction marker JSON is not published in `same` mode. Settings changes are never applied retroactively to completed jobs. Collisions and extraction errors retain the original. Allow enough disk space for source and extracted content. Multipart RAR/7z and nested extraction are experimental until field validation.

**Extraction progress and disk preflight:** Before extracting, the client reads estimated output sizes from ZIP, TAR, RAR and 7z archive metadata and compares the total plus a safety reserve against the free space on the download filesystem. When clearly insufficient, the local extraction fails with `archive_insufficient_disk_space` without modifying the original. Reading metadata of large TAR/TAR.GZ files can itself take time. Free-space estimates cannot predict concurrent writes by other processes. Nested extraction (beta) may require additional space; the initial estimate covers the outer layer only. Progress bytes and phases (`verifying`, `preflight`, `extracting`, `nested`, `publishing`) are exposed through loopback-only `GET /api/archives` and shown in the local browser UI.

### Local Web UI: history and archives

At `http://127.0.0.1:23324`, the client shows the latest 20 local download and archive records from the existing journals. Failed extractions can be queued again using **Retry extraction** (without downloading again or creating a new server lease). The most recent 100 records remain available from loopback-only `/api/history` and `/api/archives`. Download history refreshes every 12 seconds; archive status every 2 seconds.

**Web UI:** Download history, archive queue and logs start collapsed. You can hide individual entries or clear the local **display**. This preference is saved locally until a job is updated again. It does **not** delete technical download receipts, files, the archive queue or server records. Logs remain ephemeral.

**Settings** also provides extraction enablement, layout, original-file deletion and nested extraction (beta). Saving is restricted to an explicitly authorized local request. If the client is running, the worker stops gracefully and restarts with the new settings; partial transfers remain resumable. The changed policy applies only to newly started extractions and is persisted to `client.json`. Explicit CLI flags or environment variables may still override saved settings at the next process launch.

**Desktop GUI:** Use **Activity** to see local download history, archive queue and logs in a separate collapsible window. You may hide individual/all local history entries without deleting technical receipts or server records, and retry failed extractions without a new download. Refreshes every 10 seconds and on demand. Archive postprocessing policy remains server-managed through the autofetch portal.
