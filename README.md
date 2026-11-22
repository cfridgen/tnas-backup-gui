# tnas-backup-gui

Schlanke Weboberfläche für einen **TerraMaster-NAS mit TOS 4**, der nur noch als **SMB-Backup-Ziel** dient –
als Ersatz für die schwerfällige TOS-Oberfläche, angelehnt an [OpenMediaVault](https://www.openmediavault.org/).

*English summary: a lightweight replacement web UI for TerraMaster NAS devices running TOS 4 (tested on a Realtek
RTD1296 / ARM64 model) that are used purely as an SMB backup target. Single static Go binary, no dependencies on the
NAS, German UI.*

## Funktionen

| Bereich | Inhalt |
|---|---|
| **Übersicht** | System, CPU/RAM/Temperatur, Speicherbelegung, Festplattenzustand, Netzwerk, Wake-on-LAN-Status, Stand des letzten Backups (Warnung, wenn älter als 36 h) |
| **Datenträger** | Dateisysteme, Festplatten mit SMART-Werten (schlafende Platten werden nicht geweckt) |
| **Freigaben (SMB)** | anlegen / bearbeiten / entfernen, Rechte je Benutzer (lesen+schreiben / nur lesen / kein Zugriff), Gastzugang, „für alle nur lesen“, Sichtbarkeit, Papierkorb |
| **Backup-Versionen** | btrfs-Schnappschüsse anzeigen und löschen; optional schreibgeschützte Freigabe `Backup-Versionen`, um ältere Stände direkt im Finder/Explorer zu öffnen |
| **Benutzer** | anzeigen, Passwort ändern, anlegen, löschen, Administrator-Recht |
| **System** | Dienste (Samba neu starten), Protokolle, Ausschalten/Neustart |

Anmeldung mit den vorhandenen TNAS-Konten (Passwort-Hash aus `/etc/shadow`); anmelden dürfen nur Administratoren
(beim ersten Start aus der TOS-Gruppe `@admin` übernommen).

## Wie es mit TOS zusammenspielt

* TOS erzeugt die `smb.conf` bei jedem Samba-Start neu aus seiner Datenbank (`/sbin/svrcfg-all`). Dieser Generator
  stürzt bei manchen Einträgen ab und kennt kein „nur lesen“. **tnas-backup-gui übernimmt beim ersten Start alle
  bestehenden Freigaben** aus der aktuellen `smb.conf` und erzeugt die Datei danach selbst
  (Freigaben-Liste: `/etc/tnas-backup-gui/shares.json`).
* Das Samba-Startskript ruft dafür `tnas-backup-gui -render-smb` auf – schlägt das fehl, springt automatisch wieder
  der TOS-Generator ein.
* Passwortänderungen werden in `/etc/shadow`, der Samba-Passwortdatei und der TOS-Datenbank gesetzt.
* Die alte TOS-Oberfläche bleibt erreichbar (Standard: Port 8182), die neue läuft auf **Port 8181**.

## Bauen

Voraussetzung: Go ≥ 1.22.

```sh
make            # -> dist/tnas-backup-gui-linux-arm64
make dist       # -> dist/tnas-backup-gui-<version>-linux-arm64.tar.gz (Programm + Installationsskripte)
```

Für andere Architekturen `GOARCH` im Makefile anpassen (z. B. `amd64` für x86-Modelle).

## Installation (auf dem NAS, als root)

```sh
mkdir /tmp/tbg && cd /tmp/tbg
tar -xzf tnas-backup-gui-*-linux-arm64.tar.gz      # vorher per scp/ssh auf den NAS kopieren
sh install.sh ./tnas-backup-gui                     # TOS_PORT=8182 (Standard) = Port der alten Oberfläche
```

Danach: `http://<nas>:8181/`. Das Skript sichert alle geänderten Dateien nach `/opt/tnas-backup-gui/backup-<datum>/`.

### Konfiguration

Optional `/etc/tnas-backup-gui/config.json` – alle Felder mit Vorgaben siehe [`config.go`](config.go), z. B.:

```json
{ "listen": ":8181", "data_dir": "/mnt/md0", "snapshot_dir": "/mnt/md0/.snapshots", "backup_source": "OMV" }
```

### Backup-Versionen

Die Seite *Backup-Versionen* erwartet Schnappschüsse im Format `<snapshot_dir>/omv-JJJJ-MM-TT_HHMM/<Freigabe>/`
(je Freigabe ein schreibgeschütztes btrfs-Subvolume), wie sie ein nächtliches rsync-Backup von einem anderen
Server anlegt. Die Aufbewahrung steuert das Backup-Skript auf der Quelle.

### Deinstallation / Rückweg zu TOS

```sh
sh /opt/tnas-backup-gui/uninstall.sh
```

Stellt Samba-Startskript und TOS-Oberfläche (Port 8181) wieder her und lässt TOS die `smb.conf` neu erzeugen.
SSH bleibt in jedem Fall erhalten – darüber lässt sich alles reparieren.

## Sicherheit

Gedacht für das **lokale Netz** (HTTP, kein TLS). Sitzungs-Cookie `HttpOnly`/`SameSite=Strict`, CSRF-Token für alle
ändernden Aufrufe, Bremse nach 5 Fehlversuchen, keine Shell-Aufrufe mit Benutzereingaben, strenge
Content-Security-Policy. Die Oberfläche läuft als root (nötig für Samba-, Benutzer- und btrfs-Verwaltung).

## Lizenz

MIT
