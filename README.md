# autofetch client

Headless download client for the autofetch system.

The client connects to the autofetch service, retrieves download jobs, processes them locally, and reports results back to the server.

No manual server configuration is required.

---

## Features

* Headless operation (no UI)
* Secure client authentication via pairing and token
* Reliable job leasing with heartbeat mechanism
* Idempotent downloads (deduplication)
* Cross-platform support (Linux, macOS, Windows, ARM)
* Suitable for servers, NAS systems, and low-power devices

---

## Installation

1. Download the appropriate binary for your platform.
2. Extract the archive (if applicable).
3. Place the `autofetch` binary in a suitable location.

No additional dependencies are required.

---

## First Start & Pairing

On first start, the client is not yet connected to a server.

```bash
./autofetch
```

The client will print a pairing code.

Enter this code in the autofetch web interface under **Clients**.
Once approved, the client stores its credentials locally and starts processing jobs automatically.

### Re-pairing

To reset the connection and start a new pairing process:

```bash
./autofetch --re-pair
```

---

## Running as a Service

### Linux (systemd)

Create user and directories:

```bash
sudo useradd -r -s /usr/sbin/nologin autofetch
sudo mkdir -p /var/lib/autofetch
sudo chown autofetch:autofetch /var/lib/autofetch
```

Install binary:

```bash
sudo cp autofetch /usr/local/bin/autofetch
sudo chmod +x /usr/local/bin/autofetch
```

Create service file:

```
/etc/systemd/system/autofetch.service
```

```ini
[Unit]
Description=autofetch client
After=network-online.target
Wants=network-online.target

[Service]
User=autofetch
Group=autofetch
ExecStart=/usr/local/bin/autofetch
WorkingDirectory=/var/lib/autofetch
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable autofetch
sudo systemctl start autofetch
```

Logs:

```bash
journalctl -u autofetch -f
```

---

### macOS (launchd)

Install binary:

```bash
sudo cp autofetch /usr/local/bin/autofetch
sudo chmod +x /usr/local/bin/autofetch
```

Create LaunchAgent:

```
~/Library/LaunchAgents/com.autofetch.client.plist
```

Load service:

```bash
launchctl load ~/Library/LaunchAgents/com.autofetch.client.plist
```

---

### Windows

1. Place `autofetch.exe` in e.g.:

   ```
   C:\Program Files\autofetch\
   ```

2. First run (pairing):

```powershell
cd "C:\Program Files\autofetch"
.\autofetch.exe
```

3. Run as a service (recommended):

Using NSSM:

```powershell
nssm install autofetch "C:\Program Files\autofetch\autofetch.exe"
nssm start autofetch
```

---

## Security Model

* Clients authenticate using a server-issued token
* All API communication is authenticated
* Tokens can be revoked at any time
* Jobs are only assigned after successful pairing

---

## Project Structure

This repository contains **only the client**.

* The client is open source
* The autofetch server is **not part of this repository** and remains proprietary

---

## License

This project is licensed under the MIT License.

You are free to use, modify, and distribute the client software.

See the `LICENSE` file for details.
