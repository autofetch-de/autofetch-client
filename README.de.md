# autofetch-Client

Der autofetch-Client führt Downloads auf dem eigenen Gerät aus. Er wird mit einem autofetch-Konto gekoppelt, empfängt Downloadaufträge, speichert Dateien im lokalen Downloadordner und meldet das technische Ergebnis an den Server zurück.

Das Repository enthält einen grafischen Client mit Tray-Menü, eine lokale Browseroberfläche und eine Headless-/Kommandozeilenvariante.

[English documentation](README.md)

## Sprachvarianten

Jedes Release enthält zwei festsprachige Varianten, die aus derselben Codebasis und denselben Übersetzungskatalogen gebaut werden:

- `de`: deutsche Benutzeroberfläche
- `en`: englische Benutzeroberfläche

Beim Download wird die Variante anhand des Sprachsuffixes ausgewählt, zum Beispiel:

```text
autofetch-gui-windows-amd64-de-v1.1.0.exe
autofetch-headless-linux-arm64-en-v1.1.0
```

Die Sprache ändert keine API-Pfade, Konfigurationsschlüssel, technischen Fehlercodes oder Protokollbegriffe. Technische Logs bleiben in beiden Varianten Englisch, damit Supportinformationen vergleichbar bleiben.

## Verfügbare Varianten

### Grafischer Client

Der GUI-Build enthält:

- Desktop-Statusfenster
- Tray-Menü
- Pairing-Ablauf
- Downloadfortschritt
- allgemeine Einstellungen
- lokale IRC-, NickServ-, SASL- und Reverse-DCC-Einstellungen

Grafische Releaseziele:

- Windows AMD64
- macOS Intel
- macOS Apple Silicon
- Linux AMD64

### Headless-Client

Der Headless-Build ist für Server, NAS-Systeme und kleine Geräte vorgesehen. Releaseziele:

- Linux AMD64
- Linux ARM64
- Linux ARMv7
- Windows AMD64
- Windows 386
- macOS Intel
- macOS Apple Silicon

Der normale Kommandozeilenclient kann zusätzlich eine lokale Statusoberfläche auf `127.0.0.1:23324` bereitstellen. Mit `--headless` wird sie deaktiviert.

## Erster Start und Pairing

Die heruntergeladene Datei starten. Der Dateiname hängt von Sprache und Plattform ab:

```bash
./autofetch-de
```

Beim ersten Start zeigt der Client einen Kopplungscode und die Pairing-Seite an. Den Code im Portal eingeben und den Client bestätigen. Danach speichert der Client seine Zugangsdaten lokal und verarbeitet automatisch Aufträge.

Erneutes Pairing:

```bash
./autofetch-de --re-pair
```

Nur den Kopplungscode ausgeben:

```bash
./autofetch-de --print-code-only
```

## Lokale Konfiguration

Die Konfiguration liegt unterhalb des Benutzer-Konfigurationsverzeichnisses des Betriebssystems:

```text
autofetch/client.json
autofetch/irc-secrets.json
```

`client.json` enthält Serververbindung, Clientzugang, Downloadordner und nicht geheime IRC-Einstellungen. IRC-Passwörter und SASL-Zugangsdaten werden getrennt in `irc-secrets.json` gespeichert. Beide Dateien erhalten restriktive Dateirechte, soweit das Betriebssystem dies unterstützt.

Die Sprache wird nicht in der Konfiguration gespeichert. Sie ist fest in die gewählte `de`- oder `en`-Releasevariante eingebaut.

## Linux-systemd-Beispiel

Die gewählte Binärdatei unter einem stabilen lokalen Namen installieren:

```bash
sudo install -m 0755 autofetch-headless-linux-amd64-de-v1.1.0 /usr/local/bin/autofetch
```

`/etc/systemd/system/autofetch.service` anlegen:

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

Anschließend:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now autofetch
journalctl -u autofetch -f
```

## Datenschutz und externe Verbindungen

Der Client enthält keine Analyse-, Werbe-, Tracking- oder Marketingtelemetrie. Er kommuniziert nur für Pairing, Konfiguration, Auftragsabholung, Heartbeats und Ergebnismeldungen mit dem konfigurierten autofetch-Server.

Wenn Reverse DCC aktiviert ist, kann der Client Dienste zur Ermittlung der öffentlichen IP-Adresse aufrufen, um diese einem IRC-Bot mitzuteilen. Mit `AUTOFETCH_DCC_PUBLIC_IP` kann die öffentliche IP ausdrücklich vorgegeben werden. Technische Logs können Hostnamen, Channels, Dateinamen und Netzwerkfehler enthalten und sollten vor einer Weitergabe an den Support geprüft werden.

## Buildinformationen

GUI- und Headless-Dateien unterstützen:

```bash
./autofetch-de --version
```

Die Ausgabe enthält Version, Commit, Builddatum, Plattform, Architektur, Variante und feste Sprache.

## Entwicklung und Prüfung

Die gemeinsame Übersetzungsarchitektur und der manuelle Testablauf sind repository-intern dokumentiert:

- [`docs/I18N_ARCHITECTURE.md`](docs/I18N_ARCHITECTURE.md)
- [`docs/I18N_MANUAL_TEST.md`](docs/I18N_MANUAL_TEST.md)

## Lizenz

Der autofetch-Client steht unter der MIT-Lizenz. Einzelheiten stehen in `LICENSE`.
