# IRC-TLS: eng begrenzte Legacy-CN-Kompatibilität

## Motivation

Manche ältere IRC-Server liefern ein TLS-Zertifikat mit passendem Common Name (CN), aber **ohne** `subjectAltName` (SAN). Go verweigert solche Verbindungen mit `x509: certificate relies on legacy Common Name field, use SANs instead`.

Standardmäßig bleibt die volle Go-TLS-Prüfung aktiv. Ein expliziter lokaler Opt-in erlaubt ausschließlich **exakte** Hostnamen und ausschließlich Zertifikate, deren *CN genau dem aufgerufenen Host entspricht*, die **keine SAN-Erweiterung** enthalten und deren Zertifikatskette beim Betriebssystem bereits vertrauenswürdig ist. Zertifikatsgültigkeit, Server-Authentifizierungszweck und Chain of Trust werden weiterhin überprüft. Weder wird automatisch auf Plaintext gewechselt noch ein anderes Netzwerk gewählt. **Selbstsignierte/nicht vertrauenswürdige Zertifikate werden dadurch nicht akzeptiert.**

Beispiel für eine begrenzte lokale Freigabe:

```bash
AUTOFETCH_IRC_TLS_LEGACY_CN_HOSTS=irc.irchighway.net ./build/autofetch-headless
```

Mehrere Hosts sind nur durch Kommas getrennt möglich. Wildcards und IP-Adressen werden nicht als Ausnahmen akzeptiert. Bei leerer Variable ist die Ausnahme vollständig deaktiviert. Die Ausnahme gilt sowohl für ältere nummerierte XDCC-Downloads als auch für `irc_command`, aber ausschließlich beim TLS-Handshake zu diesen exakt benannten IRC-Hosts.

## Zertifikat zuvor prüfen

Das Zertifikat wird jeweils von dem IRC-Server präsentiert, den der Client tatsächlich erreicht. Vor dem Opt-in auf **dem Client-Rechner** kontrollieren (vorzugsweise zusätzlich die Angaben beim Netzwerkbetreiber verifizieren):

```bash
openssl s_client -connect irc.irchighway.net:6697 \
  -servername irc.irchighway.net -showcerts </dev/null \
  2>&1 | grep -E 'subject=|issuer=|Verify return code:'

openssl s_client -connect irc.irchighway.net:6697 \
  -servername irc.irchighway.net </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates -ext subjectAltName
```

Bei `Verify return code` ungleich 0, falschem CN, abgelaufenem Zertifikat oder nicht geklärter Serveridentität **nicht** freigeben. Eine vertrauenswürdige SAN-konforme Serveradresse oder eine Korrektur des Zertifikats beim Netzwerkbetreiber ist weiterhin die bevorzugte Lösung.

## Technische Umsetzung

Im **explizit freigegebenen** Fall verwendet der Client `tls.Config.VerifyConnection` und eine vollständige manuelle `x509.Verify`-Kettenprüfung. `InsecureSkipVerify` ist dabei technisch nur nötig, um die Go-eigene SAN-Pflicht durch diese eigene Zertifikatsprüfung zu ersetzen; es entsteht **keine ungeprüfte TLS-Verbindung**. Für alle anderen Hosts bleibt die normale TLS-Implementierung unverändert.

**Grenzen:** Die Prüfung verwendet `x509.Verify`, keine zusätzliche OCSP-/CRL-Revocation-Prüfung. DNS-Round-Robin-Backends mit anderen Zertifikaten können weiterhin scheitern. Das ist beabsichtigt; es gibt kein automatisches Umgehen von Zertifikatsfehlern.
