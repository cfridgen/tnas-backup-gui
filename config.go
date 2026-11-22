package main

import (
	"encoding/json"
	"os"
)

// Config enthält alle gerätespezifischen Einstellungen. Die Vorgaben passen zu einem
// TerraMaster mit TOS 4 (Realtek RTD1296); abweichende Werte in /etc/tnas-backup-gui/config.json.
type Config struct {
	Listen        string `json:"listen"`        // Adresse der Weboberfläche
	DataDir       string `json:"data_dir"`      // Datenvolume (Freigaben nur darunter)
	SnapshotDir   string `json:"snapshot_dir"`  // btrfs-Schnappschüsse des Backups
	BackupDir     string `json:"backup_dir"`    // Ordner, dessen Unterordner geschnappschusst werden
	BackupSource  string `json:"backup_source"` // Anzeigename der Backup-Quelle (z. B. "OMV")
	BackupMaxAgeH int    `json:"backup_max_age_h"`
	StateDir      string `json:"state_dir"`   // eigene Daten (Freigaben-Konfiguration)
	SmbConf       string `json:"smb_conf"`    // erzeugte Samba-Konfiguration
	SmbGlobal     string `json:"smb_global"`  // globale Samba-Einstellungen (Include)
	NetIface      string `json:"net_iface"`   // Netzwerkkarte für Anzeige/Wake-on-LAN
	AdminGroup    string `json:"admin_group"` // nur Mitglieder dürfen sich anmelden
	TosNasDB      string `json:"tos_nasdb"`   // TOS-Datenbank (Passwort-Abgleich für die alte GUI), leer = aus
	SessionHours  int    `json:"session_hours"`
}

func defaultConfig() Config {
	return Config{
		Listen:        ":8181",
		DataDir:       "/mnt/md0",
		SnapshotDir:   "/mnt/md0/.snapshots",
		BackupDir:     "/mnt/md0/OMV-Backup",
		BackupSource:  "OMV",
		BackupMaxAgeH: 36,
		StateDir:      "/etc/tnas-backup-gui",
		SmbConf:       "/etc/samba/smb.conf",
		SmbGlobal:     "/etc/samba/smb-extend.conf",
		NetIface:      "eth0",
		AdminGroup:    "admin",
		TosNasDB:      "/etc/base/nasdb",
		SessionHours:  8,
	}
}

func loadConfig(path string) (Config, error) {
	c := defaultConfig()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}
