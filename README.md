# autofetch client

Headless Download-Client für das autofetch-System.

Der Client verbindet sich mit dem offiziellen autofetch-Dienst,
übernimmt Download-Aufträge, sendet Heartbeats und meldet Ergebnisse
zurück.

Eine Server-Konfiguration ist nicht erforderlich.

---

## Funktionen

- Headless-Betrieb (keine Benutzeroberfläche)
- Sichere Client-Authentifizierung per Pairing und Token
- Zuverlässiges Job-Leasing mit Heartbeats
- Idempotente Downloads (Deduplizierung)
- Plattformübergreifende Binaries (Linux, macOS, Windows, Raspberry Pi)
- Geeignet für Server, NAS-Systeme und Low-Power-Geräte

---

## Installation

1. Lade das Archiv für deine Plattform herunter.
2. Entpacke es.
3. Platziere das `autofetch`-Binary an einem geeigneten Ort.

Es sind keine zusätzlichen Abhängigkeiten erforderlich.

---

## Erster Start / Pairing

Beim ersten Start ist der Client noch nicht gekoppelt.

```bash
./autofetch
Der Client gibt einen Pairing-Code aus.

Diesen Code im autofetch-Webinterface unter Clients eingeben.
Nach Freigabe speichert der Client seine Zugangsdaten lokal und
beginnt automatisch mit der Arbeit.

Erneutes Pairing
Wenn ein Client widerrufen wurde oder neu gekoppelt werden soll:

./autofetch --re-pair
Lokale Zugangsdaten werden gelöscht und ein neues Pairing gestartet.

Betrieb als Dienst
Linux (systemd)
Benutzer und Verzeichnisse anlegen
sudo useradd -r -s /usr/sbin/nologin autofetch
sudo mkdir -p /var/lib/autofetch
sudo chown autofetch:autofetch /var/lib/autofetch
Binary installieren
sudo cp autofetch /usr/local/bin/autofetch
sudo chmod +x /usr/local/bin/autofetch
Service-Datei erstellen
/etc/systemd/system/autofetch.service

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
Aktivieren und starten:

sudo systemctl daemon-reload
sudo systemctl enable autofetch
sudo systemctl start autofetch
Logs anzeigen:

journalctl -u autofetch -f
macOS (launchd)
Binary installieren:

sudo cp autofetch /usr/local/bin/autofetch
sudo chmod +x /usr/local/bin/autofetch
LaunchAgent anlegen:

~/Library/LaunchAgents/com.autofetch.client.plist

<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
 "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.autofetch.client</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/autofetch</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/autofetch.out.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/autofetch.err.log</string>
</dict>
</plist>
Laden:

launchctl load ~/Library/LaunchAgents/com.autofetch.client.plist
Windows
Binary z. B. nach
C:\Program Files\autofetch\ kopieren.

Erster Start (für Pairing):

cd "C:\Program Files\autofetch"
.\autofetch.exe
Als Dienst ausführen (empfohlen) mit NSSM:

nssm install autofetch "C:\Program Files\autofetch\autofetch.exe"
nssm start autofetch
Sicherheitsmodell (Kurzfassung)
Authentifizierung über serverseitig ausgegebene Tokens

Alle Client-API-Aufrufe sind authentifiziert

Tokens können serverseitig jederzeit widerrufen werden

Jobs werden nur nach erfolgreichem Pairing vergeben

Lizenz / License
Diese Software ist proprietär.

Die Nutzung ist ausschließlich in Verbindung mit dem offiziellen
autofetch-Dienst gestattet.

Der Nutzer ist dafür verantwortlich, dass die Verwendung dieser Software
im Einklang mit geltendem Recht und den Rechten Dritter erfolgt.

Weitere Details siehe LICENSE.txt.




# autofetch client

Headless download client for the autofetch system.

The client connects to the official autofetch service, leases jobs,
downloads media, sends heartbeats, and reports results back to the server.

No server configuration is required.

---

## Features

- Headless operation (no UI)
- Secure client authentication via pairing and token
- Robust job leasing with heartbeats
- Idempotent downloads (deduplication support)
- Cross-platform binaries (Linux, macOS, Windows, Raspberry Pi)
- Suitable for servers, NAS systems, and low-power devices

---

## Installation

1. Download the archive matching your platform.
2. Extract it.
3. Place the `autofetch` binary in a suitable directory.

No additional dependencies are required.

---

## First start / Pairing

On first start, the client is unpaired.

```bash
./autofetch
The client prints a pairing code.

Enter this code in the autofetch web interface under Clients.
Once approved, the client stores its credentials locally and starts working
automatically.

Re-pairing
If a client was revoked or should be paired again:

./autofetch --re-pair
This clears local credentials and starts a new pairing flow.

Running as a service
Linux (systemd)
1. Create user and directories
sudo useradd -r -s /usr/sbin/nologin autofetch
sudo mkdir -p /var/lib/autofetch
sudo chown autofetch:autofetch /var/lib/autofetch
2. Install binary
sudo cp autofetch /usr/local/bin/autofetch
sudo chmod +x /usr/local/bin/autofetch
3. Create service file
/etc/systemd/system/autofetch.service

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
4. Enable and start
sudo systemctl daemon-reload
sudo systemctl enable autofetch
sudo systemctl start autofetch
Logs:

journalctl -u autofetch -f
macOS (launchd)
1. Install binary
sudo cp autofetch /usr/local/bin/autofetch
sudo chmod +x /usr/local/bin/autofetch
2. Create LaunchAgent
~/Library/LaunchAgents/com.autofetch.client.plist

<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
 "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.autofetch.client</string>

  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/autofetch</string>
  </array>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <true/>

  <key>StandardOutPath</key>
  <string>/tmp/autofetch.out.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/autofetch.err.log</string>
</dict>
</plist>
3. Load service
launchctl load ~/Library/LaunchAgents/com.autofetch.client.plist
Windows
1. Install binary
Extract autofetch.exe

Place it in e.g. C:\Program Files\autofetch\

2. Run manually (first pairing)
Open PowerShell:

cd "C:\Program Files\autofetch"
.\autofetch.exe
Complete pairing in the web interface.

3. Run as a service (recommended)
Use a service wrapper such as NSSM (Non-Sucking Service Manager).

Example:

nssm install autofetch "C:\Program Files\autofetch\autofetch.exe"
nssm start autofetch
Security model (short)
Client authenticates using a server-issued token

All client API calls are authenticated

Tokens can be revoked server-side at any time

Pairing approval is required before a client receives jobs

Uninstall
Stop the service

Remove the binary

Delete the local working directory (credentials are stored locally)

## License

This software is proprietary and may only be used with the official
autofetch service.

The user is responsible for ensuring that their use of this software
complies with applicable laws and third-party rights.

For further details see LICENSE.txt.