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
autofetch-gui-windows-amd64-de-v1.2.1.exe
autofetch-headless-linux-arm64-en-v1.2.1
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


## WebUI im lokalen Netzwerk (ab v1.2.1)

**Standard:** `127.0.0.1:23324`, ohne LAN-Freigabe. Ein einfacher Headless-Start mit `--headless` schaltet die WebUI weiterhin aus. Für LAN-Zugriff ist eine **konkrete private IP-Adresse des Clients** nötig (kein `0.0.0.0`, kein öffentlicher Hostname).

Mit Passwort (empfohlen):

```bash
# Passwort als der Benutzer erzeugen, unter dem der Client läuft:
umask 077
mkdir -p "$HOME/.config/autofetch"
openssl rand -hex 24 > "$HOME/.config/autofetch/webui.password"
chmod 600 "$HOME/.config/autofetch/webui.password"

./autofetch-de --headless --webui=true \
  --webui-addr=192.168.1.50:23324 \
  --webui-password-file="$HOME/.config/autofetch/webui.password"
```

Die IP `192.168.1.50` ist durch **die tatsächliche LAN-Adresse des Client-Rechners** zu ersetzen. Im Browser `http://192.168.1.50:23324` aufrufen, Benutzername `autofetch`, Passwort aus der Passwortdatei. Die WebUI einschließlich Historie und Aktionen ist dann geschützt.

Ohne Passwort **nur mit ausdrücklicher Freigabe und in einem vollständig vertrauenswürdigen LAN**:

```bash
./autofetch-de --headless --webui=true --webui-addr=192.168.1.50:23324 --webui-lan-no-password
```

**Passwort in der WebUI einrichten oder ändern:** Unter **Einstellungen → WebUI-Passwort** befinden sich zwei **optionale** Passwortfelder. Beide leer lassen, um das bestehende Passwort **nicht zu ändern** (bei bislang passwortlosem LAN-Zugriff bleibt dieser Modus dann bestehen). Zum Einrichten oder Ändern das neue Passwort zweimal eingeben und mit **Speichern** am Ende des Formulars bestätigen. Die Browser-Anmeldung erfolgt immer mit **Benutzername `autofetch`** und dem neu gesetzten Passwort. Die Änderung ist sofort aktiv, auch ohne Neustart. Beim erstmaligen Einrichten wird die private Datei `webui.password` (Dateirechte `0600`) neben `client.json` erstellt; in der JSON-Datei steht nur der Dateipfad. Spätere Passwortänderungen ersetzen die geschützte Passwortdatei atomar. Falls `--webui-lan-no-password` noch im Startskript oder der systemd-Konfiguration steht, hat die eingerichtete Passwortdatei bei Neustarts Vorrang; das Flag sollte trotzdem entfernt werden. **Achtung:** Die Passworteingabe und HTTP-Basic-Anmeldung sind über HTTP nicht verschlüsselt. Nur im vertrauenswürdigen LAN oder lokal über SSH-Tunnel verwenden.

Alternativ für systemd: `Environment=AUTOFETCH_WEBUI=true`, `Environment=AUTOFETCH_WEBUI_ADDR=192.168.1.50:23324`, `Environment=AUTOFETCH_WEBUI_PASSWORD_FILE=/var/lib/autofetch/webui.password` (oder ausdrücklich `Environment=AUTOFETCH_WEBUI_LAN_NO_PASSWORD=true`). Die Passwortdatei muss für den systemd-Dienstbenutzer lesbar und für andere Benutzer nicht zugänglich sein (`0600`); in `client.json` wird nur **der Pfad** gespeichert. Die Einstellungen sind auch über gleichnamige CLI-Flags möglich.

**Sicherheit:** Die Browseroberfläche verwendet HTTP, **kein HTTPS**. Die HTTP-Basic-Anmeldung schützt vor nicht autorisierten Zugriffen, jedoch **nicht** vor Mitlesen des Passworts im lokalen Netzwerk. Nur in einem vertrauenswürdigen LAN nutzen; für unsichere Netze per SSH-Tunnel oder VPN auf `127.0.0.1` zugreifen. Keinen Router-Port freigeben, keine Internet-Exposition. Ein sicherer Host- und Origin-Check verhindert nicht alle Risiken eines kompromittierten LANs.

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
sudo install -m 0755 autofetch-headless-linux-amd64-de-v1.2.1 /usr/local/bin/autofetch
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

## Optionale Archivextraktion

Die Extraktion ist standardmäßig **ausgeschaltet**. ZIP, TAR, RAR und 7z wurden inzwischen im lokalen Praxistest bestätigt; RAR und 7z benötigen weiterhin `7zz` oder `7z` im PATH. Ein Entpackfehler ändert den erfolgreichen Downloadstatus nicht. Der lokale Status ist über `GET /api/archives` (nur Loopback) abrufbar.

Die Ablage- und Bereinigungsoptionen sind unabhängig voneinander und auch in `client.json` hinterlegbar:

```json
{
  "extract_archives": true,
  "extract_layout": "same",
  "extract_delete_original": true,
  "extract_nested_depth": 0
}
```

- `extract_layout`: `separate` (Standard, `<Dateiname>.extracted/`) oder `same` (intelligente Ablage im Download-Verzeichnis). Bei **einer** Nutzdatei entsteht direkt die Datei (`Film.rar` → `Film.mkv`), bei **mehreren** Nutzdateien ein Ordner mit dem Archivnamen ohne Endung (`Sammlung.rar` → `Sammlung/`). Enthält das Archiv bereits einen gemeinsamen Hauptordner, wird dieser übernommen. Gleichnamige Dateien oder Ordner werden nie überschrieben.
- `extract_delete_original`: `false` (Standard) oder `true`. Löscht das Original **erst nach** vollständiger Extraktion, sicherer Veröffentlichung und dauerhaftem Abschlussnachweis; bei mehrteiligen Archiven auch die zugehörigen Volumes.
- `extract_nested_depth`: `0` (Standard); `1` bis `3` sind **BETA**. Verschachtelte Archive bleiben optional und noch nicht realitätsnah durchgetestet.

**Hinweis zu Startparametern:** Ohne explizite Archiv-Flags gelten die Werte der lokalen `client.json` (bzw. die darüberliegenden Umgebungsvariablen). Insbesondere ist ein zuvor gespeichertes `extract_delete_original: true` weiterhin aktiv. Für einen Start ohne automatische Extraktion/Löschung ausdrücklich `--extract-archives=false --extract-delete-original=false` setzen. Der Client protokolliert die effektiven Archivoptionen beim Start.

Entsprechende Startparameter: `--extract-archives --extract-layout=same --extract-delete-original`; alternativ `AUTOFETCH_EXTRACT_LAYOUT=same` und `AUTOFETCH_EXTRACT_DELETE_ORIGINAL=1`. Die JSON-Dateien des Download-Journals und der Extraktionshistorie bleiben **bewusst** erhalten, damit ein später erneut zugewiesener Auftrag keinen unnötigen Download auslöst. Die temporäre JSON-Markierung der Extraktion wird bei `same` nicht in das Download-Verzeichnis übernommen. Ein bereits abgeschlossener Vorgang wird durch spätere Einstellungsänderungen nicht rückwirkend verändert. Bei einer Kollision oder einem fehlerhaften Archiv bleibt das Original erhalten. Die Verarbeitung benötigt zunächst genug Speicher für Archiv und extrahierte Inhalte. Mehrteilige RAR/7z- und verschachtelte Archive sollten bis zum Praxistest als Beta betrachtet werden.

**Extraktionsfortschritt und Speicherplatz:** Vor dem Entpacken liest der Client die Dateigrößen aus ZIP-, TAR-, RAR- und 7z-Archivinformationen und vergleicht die geschätzte Ausgabegröße plus Sicherheitsreserve mit dem freien Platz auf dem Dateisystem des Download-Ordners. Bei offensichtlich zu wenig Platz lautet der Fehler `archive_insufficient_disk_space`; das Original wird nicht verändert. Insbesondere bei großen TAR/TAR.GZ-Dateien kann das vollständige Einlesen der Metadaten Zeit benötigen. Eine Prüfung kann keinen gleichzeitigen Speicherverbrauch durch andere Prozesse vorhersagen. Die verschachtelte Extraktion (Beta) kann zusätzlichen Platz benötigen; die erste Schätzung bezieht sich nur auf die äußere Ebene. Verarbeitete Bytes, geschätztes Gesamtvolumen und die Phasen `verifying`, `preflight`, `extracting`, `nested` und `publishing` stehen unter `GET /api/archives` zur Verfügung und werden während der Verarbeitung in der lokalen Weboberfläche angezeigt.

### Lokale Weboberfläche: Historie und Archive

Unter `http://127.0.0.1:23324` zeigt der Client die letzten 20 lokalen Downloadaufträge und Archivverarbeitungen aus den bestehenden Journals. Fehlgeschlagene Extraktionen können dort mit **Extraktion wiederholen** erneut eingereiht werden (ohne erneuten Download oder Server-Lease). Die vollständigen letzten 100 lokalen Datensätze bleiben über `/api/history` und `/api/archives` verfügbar. Die Historie wird alle 12 Sekunden, die Archivansicht alle 2 Sekunden aktualisiert. Beide Datenschnittstellen sind auf Loopback-Zugriffe beschränkt.

**Weboberfläche:** Downloadhistorie, Archivwarteschlange und Logs sind standardmäßig eingeklappt. Einträge lassen sich einzeln oder gesammelt aus der **lokalen Ansicht** entfernen. Diese Auswahl bleibt auf dem Client erhalten, bis sich ein Auftrag erneut ändert; das zugehörige technische Download-Journal, heruntergeladene Dateien, Archiv-Warteschlange und die Serverhistorie werden dadurch **nicht** gelöscht. Die Logs bleiben flüchtig.

Unter **Einstellungen** können außerdem automatische Extraktion, Ablageort, Originaldatei-Löschung und die verschachtelte Extraktion (Beta) geändert werden. Für diese Aktion ist eine lokale, ausdrücklich autorisierte Anfrage erforderlich. Bei aktivem Client wird der Worker kontrolliert beendet und mit der neuen Konfiguration gestartet; unvollständige Transfers behalten ihre Resume-Dateien. Die neue Konfiguration gilt nur für künftig gestartete Extraktionen und wird in `client.json` gespeichert. Explizite CLI-Flags und Umgebungsvariablen können beim nächsten Programmstart weiterhin Vorrang haben.

**Desktop-GUI:** Über **Aktivitäten** lassen sich lokale Downloadhistorie, Archivwarteschlange und Logs in einem separaten, einklappbaren Fenster anzeigen. Einzelne bzw. alle Historieneinträge können aus der lokalen Ansicht ausgeblendet werden (technische Belege und Serverhistorie bleiben erhalten). Fehlgeschlagene Extraktionen lassen sich ohne erneuten Download erneut einreihen. Aktualisierung alle 10 Sekunden sowie per Schaltfläche. Die fachlichen Archivrichtlinien werden weiterhin ausschließlich im autofetch-Portal eingestellt.
