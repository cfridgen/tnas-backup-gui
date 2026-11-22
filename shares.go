package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Share ist eine SMB-Freigabe. Die Liste liegt in <state_dir>/shares.json und ist die
// einzige Quelle für die erzeugte smb.conf (der TOS-Generator wird nicht mehr benutzt).
type Share struct {
	Name     string            `json:"name"`
	Path     string            `json:"path"`
	Comment  string            `json:"comment"`
	Browse   bool              `json:"browsable"`
	Guest    bool              `json:"guest"`            // Gastzugang ohne Passwort
	ReadOnly bool              `json:"read_only"`        // für alle nur lesend (überstimmt Benutzerrechte)
	Recycle  bool              `json:"recycle"`          // Papierkorb (#recycle)
	Users    map[string]string `json:"users"`            // Benutzer -> "rw" | "ro"
	System   string            `json:"system,omitempty"` // z. B. "versions" für die Backup-Versionen-Freigabe
}

const versionsShare = "Backup-Versionen"

var shareNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)

type shareStore struct {
	mu  sync.Mutex
	cfg *Config
}

func (st *shareStore) file() string { return filepath.Join(st.cfg.StateDir, "shares.json") }

func (st *shareStore) load() ([]Share, error) {
	b, err := os.ReadFile(st.file())
	if os.IsNotExist(err) {
		return importSmbConf(st.cfg.SmbConf, st.cfg)
	}
	if err != nil {
		return nil, err
	}
	var list []Share
	err = json.Unmarshal(b, &list)
	return list, err
}

func (st *shareStore) save(list []Share) error {
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(st.cfg.StateDir, 0700); err != nil {
		return err
	}
	return writeFileAtomic(st.file(), b, 0600)
}

// ---------- Import der bestehenden (TOS-)smb.conf beim ersten Start ----------

func importSmbConf(path string, cfg *Config) ([]Share, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []Share
	var cur *Share
	var vals map[string]string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Path = vals["path"]
		cur.Comment = vals["comment"]
		if cur.Comment == "System default share" {
			cur.Comment = ""
		}
		cur.Browse = yes(vals["browsable"], vals["browseable"], "yes")
		cur.Guest = yes(vals["public"], vals["guest ok"], "no")
		cur.Recycle = strings.Contains(vals["vfs object"]+vals["vfs objects"], "recycle")
		cur.Users = map[string]string{}
		writers := splitList(vals["write list"])
		for _, u := range splitList(vals["valid users"]) {
			if contains(writers, u) {
				cur.Users[u] = "rw"
			} else {
				cur.Users[u] = "ro"
			}
		}
		delete(cur.Users, "nobody")
		// TOS-Systemordner und Platzhalter nicht übernehmen
		base := filepath.Base(cur.Path)
		if cur.Path != "" && insideDir(cfg.DataDir, cur.Path) && cur.Name != "appdata" && !strings.HasPrefix(base, ".") {
			list = append(list, *cur)
		}
		cur = nil
	}
	for _, raw := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(raw)
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		if l[0] == '[' && strings.HasSuffix(l, "]") {
			flush()
			name := l[1 : len(l)-1]
			if n := strings.ToLower(name); n != "global" && n != "homes" && n != "printers" {
				cur = &Share{Name: name}
				vals = map[string]string{}
			}
			continue
		}
		if cur != nil {
			if k, v, ok := strings.Cut(l, "="); ok {
				vals[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
	}
	flush()
	return list, nil
}

func yes(a, b, def string) bool {
	v := strings.ToLower(a)
	if v == "" {
		v = strings.ToLower(b)
	}
	if v == "" {
		v = def
	}
	return v == "yes" || v == "true" || v == "1"
}

func splitList(s string) []string {
	var res []string
	for _, x := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if x != "" {
			res = append(res, x)
		}
	}
	return res
}

// ---------- smb.conf erzeugen ----------

func renderSmbConf(cfg *Config, list []Share) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Erzeugt von tnas-backup-gui %s am %s – NICHT von Hand ändern.\n", version, time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "# Freigaben werden in %s verwaltet (Weboberfläche).\n\n", filepath.Join(cfg.StateDir, "shares.json"))
	b.WriteString("[global]\n")
	if cfg.SmbGlobal != "" {
		fmt.Fprintf(&b, "include = %s\n", cfg.SmbGlobal)
	}
	b.WriteString("\n")
	for _, s := range list {
		fmt.Fprintf(&b, "[%s]\n", s.Name)
		if s.Comment != "" {
			fmt.Fprintf(&b, "comment = %s\n", strings.ReplaceAll(s.Comment, "\n", " "))
		}
		fmt.Fprintf(&b, "path = %s\n", s.Path)
		fmt.Fprintf(&b, "browseable = %s\n", yn(s.Browse))
		b.WriteString("inherit permissions = yes\n")
		var rw, ro []string
		for u, a := range s.Users {
			if a == "rw" {
				rw = append(rw, u)
			} else if a == "ro" {
				ro = append(ro, u)
			}
		}
		sort.Strings(rw)
		sort.Strings(ro)
		switch {
		case s.Guest:
			b.WriteString("guest ok = yes\n")
			fmt.Fprintf(&b, "read only = %s\n", yn(s.ReadOnly))
		case len(rw)+len(ro) == 0:
			b.WriteString("valid users = nobody\n") // niemand – bewusst gesperrt
			b.WriteString("read only = yes\n")
		default:
			fmt.Fprintf(&b, "valid users = %s\n", strings.Join(append(append([]string{}, rw...), ro...), ","))
			b.WriteString("read only = yes\n")
			if len(rw) > 0 && !s.ReadOnly {
				fmt.Fprintf(&b, "write list = %s\n", strings.Join(rw, ","))
			}
		}
		if s.Recycle && !s.ReadOnly {
			b.WriteString("vfs objects = recycle\nrecycle:repository = #recycle\nrecycle:keeptree = yes\nrecycle:versions = yes\nrecycle:directory_mode = 0777\nrecycle:exclude_dir = #recycle\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func yn(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// applySmb schreibt die smb.conf (mit Sicherung beim ersten Mal) und lädt Samba neu.
func (app *App) applySmb(list []Share) error {
	conf := renderSmbConf(app.cfg, list)
	tmp := app.cfg.SmbConf + ".neu"
	if err := os.WriteFile(tmp, []byte(conf), 0644); err != nil {
		return err
	}
	if _, err := run(15*time.Second, "", "testparm", "-s", tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("Samba-Konfiguration ungültig: %v", err)
	}
	os.Remove(tmp)
	orig := app.cfg.SmbConf + ".vor-tnas-backup-gui"
	if _, err := os.Stat(orig); os.IsNotExist(err) {
		if old, err := os.ReadFile(app.cfg.SmbConf); err == nil {
			_ = os.WriteFile(orig, old, 0644)
		}
	}
	if err := writeFileAtomic(app.cfg.SmbConf, []byte(conf), 0644); err != nil {
		return err
	}
	if pidOf("smbd") {
		_, _ = run(15*time.Second, "", "smbcontrol", "all", "reload-config")
	}
	return nil
}

// ---------- API ----------

func (app *App) apiShares(w http.ResponseWriter, r *http.Request, s *session) {
	app.shares.mu.Lock()
	list, err := app.shares.load()
	app.shares.mu.Unlock()
	if err != nil {
		writeErr(w, 500, "Freigaben konnten nicht gelesen werden: %v", err)
		return
	}
	type row struct {
		Share
		Exists bool `json:"exists"`
	}
	var res []row
	for _, sh := range list {
		_, err := os.Stat(sh.Path)
		res = append(res, row{sh, err == nil})
	}
	writeJSON(w, 200, map[string]any{"shares": res, "users": smbUsers(), "data_dir": app.cfg.DataDir})
}

func (app *App) validateShare(sh *Share, list []Share, oldName string) error {
	sh.Name = strings.TrimSpace(sh.Name)
	if !shareNameRe.MatchString(sh.Name) {
		return fmt.Errorf("Name: 1–64 Zeichen, Buchstaben/Ziffern/Leerzeichen/._- (keine Umlaute)")
	}
	if l := strings.ToLower(sh.Name); l == "global" || l == "homes" || l == "printers" || l == "ipc$" {
		return fmt.Errorf("Name ist reserviert")
	}
	for _, x := range list {
		if strings.EqualFold(x.Name, sh.Name) && !strings.EqualFold(x.Name, oldName) {
			return fmt.Errorf("Eine Freigabe „%s“ gibt es schon", x.Name)
		}
	}
	sh.Path = filepath.Clean(strings.TrimSpace(sh.Path))
	if !insideDir(app.cfg.DataDir, sh.Path) {
		return fmt.Errorf("Pfad muss innerhalb von %s liegen", app.cfg.DataDir)
	}
	if strings.ContainsAny(sh.Path, "\n\r") || strings.ContainsAny(sh.Comment, "\n\r[]") {
		return fmt.Errorf("Unzulässige Zeichen")
	}
	known := smbUsers()
	for u, a := range sh.Users {
		if a != "rw" && a != "ro" {
			delete(sh.Users, u)
			continue
		}
		if !contains(known, u) {
			return fmt.Errorf("Unbekannter Benutzer: %s", u)
		}
	}
	return nil
}

func (app *App) apiShareSave(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		OldName string `json:"old_name"`
		Share   Share  `json:"share"`
		Create  bool   `json:"create_dir"`
	}
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage: %v", err)
		return
	}
	if req.Share.Users == nil {
		req.Share.Users = map[string]string{}
	}
	app.shares.mu.Lock()
	defer app.shares.mu.Unlock()
	list, err := app.shares.load()
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	if err := app.validateShare(&req.Share, list, req.OldName); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	if _, err := os.Stat(req.Share.Path); os.IsNotExist(err) {
		if !req.Create {
			writeErr(w, 400, "Ordner %s existiert nicht", req.Share.Path)
			return
		}
		// neuer Ordner direkt unter dem Datenvolume als btrfs-Subvolume (eigene Schnappschüsse möglich)
		if filepath.Dir(req.Share.Path) == filepath.Clean(app.cfg.DataDir) {
			_, err = run(30*time.Second, "", "btrfs", "subvolume", "create", req.Share.Path)
		} else {
			err = os.MkdirAll(req.Share.Path, 0777)
		}
		if err != nil {
			writeErr(w, 500, "Ordner konnte nicht angelegt werden: %v", err)
			return
		}
		_ = os.Chmod(req.Share.Path, 0777)
	}
	var out []Share
	replaced := false
	for _, x := range list {
		if req.OldName != "" && strings.EqualFold(x.Name, req.OldName) {
			req.Share.System = x.System
			out = append(out, req.Share)
			replaced = true
		} else {
			out = append(out, x)
		}
	}
	if !replaced {
		out = append(out, req.Share)
	}
	if err := app.applySmb(out); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	if err := app.shares.save(out); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Freigabe gespeichert: %s -> %s", s.user, req.Share.Name, req.Share.Path)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (app *App) apiShareDelete(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ Name string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	app.shares.mu.Lock()
	defer app.shares.mu.Unlock()
	list, err := app.shares.load()
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	var out []Share
	for _, x := range list {
		if !strings.EqualFold(x.Name, req.Name) {
			out = append(out, x)
		}
	}
	if len(out) == len(list) {
		writeErr(w, 404, "Freigabe nicht gefunden")
		return
	}
	if err := app.applySmb(out); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	if err := app.shares.save(out); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Freigabe entfernt (Daten bleiben): %s", s.user, req.Name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// setVersionsShare schaltet die schreibgeschützte Freigabe auf das Schnappschuss-Verzeichnis.
func (app *App) setVersionsShare(on bool, users []string) error {
	app.shares.mu.Lock()
	defer app.shares.mu.Unlock()
	list, err := app.shares.load()
	if err != nil {
		return err
	}
	var out []Share
	for _, x := range list {
		if x.System != "versions" {
			out = append(out, x)
		}
	}
	if on {
		u := map[string]string{}
		for _, x := range users {
			u[x] = "ro"
		}
		out = append(out, Share{Name: versionsShare, Path: app.cfg.SnapshotDir, Comment: "Ältere Stände des Backups (nur lesen)",
			Browse: true, ReadOnly: true, Users: u, System: "versions"})
	}
	if err := app.applySmb(out); err != nil {
		return err
	}
	return app.shares.save(out)
}

func (app *App) versionsShareOn() bool {
	app.shares.mu.Lock()
	defer app.shares.mu.Unlock()
	list, _ := app.shares.load()
	for _, x := range list {
		if x.System == "versions" {
			return true
		}
	}
	return false
}
