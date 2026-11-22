package main

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Service struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Running bool   `json:"running"`
}

func services() []Service {
	return []Service{
		{"smbd", "Samba (SMB-Freigaben)", pidOf("smbd")},
		{"sshd", "SSH", pidOf("sshd")},
		{"node_exporter", "Prometheus node_exporter", pidOf("node_exporter")},
		{"crond", "Zeitplaner (cron)", pidOf("crond")},
	}
}

func (app *App) apiServiceRestart(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ Name string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	if req.Name != "samba" {
		writeErr(w, 400, "Nur Samba kann hier neu gestartet werden")
		return
	}
	if _, err := run(60*time.Second, "", "/etc/init.d/samba", "restart"); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Samba neu gestartet", s.user)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- Backup-Versionen (Schnappschüsse) ----------

func (app *App) apiSnapshots(w http.ResponseWriter, r *http.Request, s *session) {
	writeJSON(w, 200, map[string]any{
		"snapshots": snapshots(app.cfg), "versions_share": app.versionsShareOn(),
		"source": app.cfg.BackupSource, "share_name": versionsShare,
	})
}

func (app *App) apiSnapshotDelete(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ Name string }
	if err := readBody(r, &req); err != nil || !snapRe.MatchString(req.Name) {
		writeErr(w, 400, "Ungültiger Name")
		return
	}
	dir := filepath.Join(app.cfg.SnapshotDir, req.Name)
	subs, err := os.ReadDir(dir)
	if err != nil {
		writeErr(w, 404, "Schnappschuss nicht gefunden")
		return
	}
	for _, e := range subs {
		if _, err := run(120*time.Second, "", "btrfs", "subvolume", "delete", filepath.Join(dir, e.Name())); err != nil {
			writeErr(w, 500, "%v", err)
			return
		}
	}
	if err := os.Remove(dir); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Schnappschuss gelöscht: %s", s.user, req.Name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (app *App) apiVersionsShare(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ On bool }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	if err := app.setVersionsShare(req.On, app.auth.admins()); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Freigabe %s: %v", s.user, versionsShare, req.On)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- Energie ----------

func (app *App) apiPower(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ Action string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	var cmd string
	switch req.Action {
	case "reboot":
		cmd = "reboot"
	case "poweroff":
		cmd = "poweroff"
	default:
		writeErr(w, 400, "Unbekannte Aktion")
		return
	}
	app.logf("%s: %s ausgelöst", s.user, cmd)
	writeJSON(w, 200, map[string]bool{"ok": true})
	go func() {
		time.Sleep(2 * time.Second)
		_, _ = run(10*time.Second, "", "sync")
		_, _ = run(30*time.Second, "", cmd)
	}()
}

// ---------- Protokolle ----------

var logFiles = map[string]string{
	"gui":      "/var/log/tnas-backup-gui.log",
	"messages": "/var/log/messages",
	"samba":    "/var/log/log.smbd",
	"cloud":    cloudLog,
}

func tail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{"(" + err.Error() + ")"}
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n*2 {
			lines = lines[len(lines)-n:]
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (app *App) apiLogs(w http.ResponseWriter, r *http.Request, s *session) {
	name := r.URL.Query().Get("name")
	p, ok := logFiles[name]
	if !ok {
		writeErr(w, 400, "Unbekanntes Protokoll")
		return
	}
	writeJSON(w, 200, map[string]any{"name": name, "path": p, "lines": tail(p, 300)})
}

func (app *App) logf(format string, a ...any) {
	app.logger.Printf(format, a...)
}

func (app *App) apiSession(w http.ResponseWriter, r *http.Request) {
	s := app.auth.current(r)
	if s == nil {
		s = app.auth.autoLogin(w, r)
	}
	host, _ := os.Hostname()
	if s == nil {
		writeJSON(w, 200, map[string]any{"user": nil, "hostname": host, "version": version})
		return
	}
	writeJSON(w, 200, map[string]any{"user": s.user, "csrf": s.csrf, "hostname": host, "version": version,
		"source": app.cfg.BackupSource, "tos_gui": strings.TrimSpace(readTrim(filepath.Join(app.cfg.StateDir, "tos-gui-url")))})
}
