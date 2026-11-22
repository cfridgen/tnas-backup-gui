#!/bin/sh
# Installation von tnas-backup-gui auf einem TerraMaster mit TOS 4 (als root auf dem NAS ausführen).
#   sh install.sh <pfad-zur-binärdatei>
# Was passiert:
#   - Programm nach /opt/tnas-backup-gui, Startskript /etc/init.d/tnas-backup-gui (Autostart)
#   - alte TOS-Oberfläche (nginx) von Port 8181 auf 8182 (bzw. TOS_PORT), neue GUI auf 8181
#   - /etc/init.d/samba: smb.conf kommt künftig von der neuen GUI (Rückfall auf TOS-Generator bei Fehler)
#   - Sicherungen aller geänderten Dateien unter /opt/tnas-backup-gui/backup-<datum>/
set -e
BIN=${1:-./tnas-backup-gui}
TOS_PORT=${TOS_PORT:-8182}
DIR=/opt/tnas-backup-gui
BK=$DIR/backup-$(date +%Y%m%d-%H%M%S)

[ "$(id -u)" = 0 ] || { echo "Bitte als root ausführen"; exit 1; }
[ -f "$BIN" ] || { echo "Binärdatei $BIN nicht gefunden"; exit 1; }
mkdir -p $DIR $BK /etc/tnas-backup-gui

# Sicherungen
for f in /etc/init.d/samba /etc/nginx/nginx.conf /etc/samba/smb.conf; do
    [ -f $f ] && cp -p $f $BK/
done
echo "Sicherungen: $BK"

# Programm + Startskript
cp "$BIN" $DIR/tnas-backup-gui.new && chmod 755 $DIR/tnas-backup-gui.new && mv $DIR/tnas-backup-gui.new $DIR/tnas-backup-gui
cat "$(dirname "$0")/tnas-backup-gui.init" > /etc/init.d/tnas-backup-gui
chmod 755 /etc/init.d/tnas-backup-gui
cp "$(dirname "$0")/uninstall.sh" $DIR/uninstall.sh 2>/dev/null || true

# optional: rclone (+ CA-Zertifikate) für das Cloud-Backup, wenn neben install.sh abgelegt
for f in rclone cacert.pem; do
    if [ -f "$(dirname "$0")/$f" ]; then
        cp "$(dirname "$0")/$f" $DIR/$f.new && mv $DIR/$f.new $DIR/$f
        [ $f = rclone ] && chmod 755 $DIR/rclone && echo "rclone installiert: $($DIR/rclone version 2>/dev/null | head -1)"
    fi
done

# Samba: TOS-Generator durch die GUI ersetzen (mit Rückfall)
if grep -q '^    /sbin/svrcfg-all -c smb$' /etc/init.d/samba; then
    sed -i 's#^    /sbin/svrcfg-all -c smb$#    '$DIR'/tnas-backup-gui -render-smb -config /etc/tnas-backup-gui/config.json || /sbin/svrcfg-all -c smb  \# tnas-backup-gui#' /etc/init.d/samba
    echo "Samba-Startskript angepasst"
fi

# alte TOS-Oberfläche auf anderen Port
if grep -q 'listen[[:space:]]*8181;' /etc/nginx/nginx.conf; then
    sed -i "s/listen\([[:space:]]*\)8181;/listen\1$TOS_PORT;/" /etc/nginx/nginx.conf
    echo "TOS-Oberfläche -> Port $TOS_PORT"
    /etc/init.d/nginx restart >/dev/null 2>&1 || true
fi
echo "http://$(hostname):$TOS_PORT/" > /etc/tnas-backup-gui/tos-gui-url

/etc/init.d/tnas-backup-gui enable
/etc/init.d/tnas-backup-gui restart 2>/dev/null || /etc/init.d/tnas-backup-gui start
sleep 2
if netstat -tln 2>/dev/null | grep -q ':8181 '; then
    echo "Fertig: neue Oberfläche auf http://$(hostname):8181/"
else
    echo "WARNUNG: Port 8181 lauscht nicht – siehe /var/log/tnas-backup-gui.log"
fi
