// tnas-backup-gui – schlanke Weboberfläche für einen TerraMaster-NAS (TOS 4), der nur noch als
// SMB-Backup-Ziel dient. Ersetzt die TOS-Oberfläche; angelehnt an OpenMediaVault.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
)

var version = "dev"

//go:embed web
var webFS embed.FS

type App struct {
	cfg    *Config
	auth   *authState
	shares *shareStore
	logger *log.Logger
}

func main() {
	cfgPath := flag.String("config", "/etc/tnas-backup-gui/config.json", "Konfigurationsdatei")
	renderSmb := flag.Bool("render-smb", false, "nur smb.conf aus der Freigaben-Liste erzeugen und beenden (für das Samba-Startskript)")
	showVersion := flag.Bool("version", false, "Version anzeigen")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("Konfiguration: %v", err)
	}
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		log.Fatalf("%v", err)
	}
	app := &App{cfg: &cfg, shares: &shareStore{cfg: &cfg}}
	app.auth = newAuth(app.cfg)

	var logOut io.Writer = os.Stderr
	if f, err := os.OpenFile(logFiles["gui"], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640); err == nil {
		logOut = io.MultiWriter(os.Stderr, f)
	}
	app.logger = log.New(logOut, "", log.LstdFlags)
	app.auth.logf = app.logf

	if *renderSmb {
		// Aufruf aus /etc/init.d/samba: bei Fehler Exitcode != 0, dann nimmt das Skript den TOS-Generator
		list, err := app.shares.load()
		if err != nil {
			log.Fatalf("Freigaben: %v", err)
		}
		if err := writeFileAtomic(cfg.SmbConf, []byte(renderSmbConf(app.cfg, list)), 0644); err != nil {
			log.Fatalf("smb.conf: %v", err)
		}
		if _, err := os.Stat(app.shares.file()); os.IsNotExist(err) {
			_ = app.shares.save(list) // Import beim ersten Lauf festschreiben
		}
		return
	}

	// Erststart: bestehende Freigaben übernehmen und festschreiben
	app.shares.mu.Lock()
	if _, err := os.Stat(app.shares.file()); os.IsNotExist(err) {
		if list, err := app.shares.load(); err == nil {
			_ = app.shares.save(list)
			app.logf("Erststart: %d Freigaben aus %s übernommen", len(list), cfg.SmbConf)
		}
	}
	app.shares.mu.Unlock()
	app.logf("tnas-backup-gui %s startet auf %s (Admins: %v)", version, cfg.Listen, app.auth.admins())

	mux := http.NewServeMux()
	a := app.auth.require
	mux.HandleFunc("GET /api/session", app.apiSession)
	mux.HandleFunc("POST /api/login", app.auth.login)
	mux.HandleFunc("POST /api/logout", app.auth.logout)
	mux.HandleFunc("GET /api/dashboard", a(app.apiDashboard))
	mux.HandleFunc("GET /api/disks", a(app.apiDisks))
	mux.HandleFunc("GET /api/shares", a(app.apiShares))
	mux.HandleFunc("POST /api/shares/save", a(app.apiShareSave))
	mux.HandleFunc("POST /api/shares/delete", a(app.apiShareDelete))
	mux.HandleFunc("GET /api/users", a(app.apiUsers))
	mux.HandleFunc("POST /api/users/password", a(app.apiUserPassword))
	mux.HandleFunc("POST /api/users/create", a(app.apiUserCreate))
	mux.HandleFunc("POST /api/users/delete", a(app.apiUserDelete))
	mux.HandleFunc("POST /api/users/admin", a(app.apiUserAdmin))
	mux.HandleFunc("GET /api/snapshots", a(app.apiSnapshots))
	mux.HandleFunc("POST /api/snapshots/delete", a(app.apiSnapshotDelete))
	mux.HandleFunc("POST /api/snapshots/share", a(app.apiVersionsShare))
	mux.HandleFunc("POST /api/services/restart", a(app.apiServiceRestart))
	mux.HandleFunc("POST /api/power", a(app.apiPower))
	mux.HandleFunc("GET /api/logs", a(app.apiLogs))
	mux.HandleFunc("GET /api/devices", a(app.apiDevices))
	mux.HandleFunc("POST /api/devices/revoke", a(app.apiDeviceRevoke))
	mux.HandleFunc("GET /api/files/roots", a(app.apiFileRoots))
	mux.HandleFunc("GET /api/files/list", a(app.apiFileList))
	mux.HandleFunc("GET /api/files/search", a(app.apiFileSearch))
	mux.HandleFunc("GET /api/files/view", a(app.apiFileView))
	mux.HandleFunc("GET /api/files/raw", a(app.apiFileRaw))
	mux.HandleFunc("GET /api/files/history", a(app.apiFileHistory))

	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))

	srv := &http.Server{Addr: cfg.Listen, Handler: secHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func secHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:")
		h.ServeHTTP(w, r)
	})
}
