#!/bin/sh
# Entfernt tnas-backup-gui und stellt den TOS-Zustand wieder her (als root ausführen).
#   sh /opt/tnas-backup-gui/uninstall.sh
# Die Freigaben-Liste (/etc/tnas-backup-gui) bleibt zur Sicherheit liegen.
DIR=/opt/tnas-backup-gui
TOS_PORT=${TOS_PORT:-8182}

/etc/init.d/tnas-backup-gui stop 2>/dev/null
/etc/init.d/tnas-backup-gui disable 2>/dev/null
rm -f /etc/init.d/tnas-backup-gui

# Samba wieder mit TOS-Generator
sed -i 's#^    '$DIR'/tnas-backup-gui -render-smb .*$#    /sbin/svrcfg-all -c smb#' /etc/init.d/samba

# TOS-Oberfläche zurück auf 8181
sed -i "s/listen\([[:space:]]*\)$TOS_PORT;/listen\18181;/" /etc/nginx/nginx.conf
/etc/init.d/nginx restart >/dev/null 2>&1

# smb.conf wieder von TOS erzeugen lassen
/sbin/svrcfg-all -c smb && smbcontrol all reload-config
echo "tnas-backup-gui entfernt. Programmordner $DIR (mit Sicherungen) bitte bei Bedarf von Hand löschen."
