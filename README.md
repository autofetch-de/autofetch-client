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
