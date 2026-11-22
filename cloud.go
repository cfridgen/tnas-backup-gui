package main

// Cloud-Backup: Freigaben (aus dem neuesten Backup-Schnappschuss) mit rclone nach OneDrive oder Google Drive
// sichern – optional verschlüsselt (rclone crypt) und mit datierten Versionen geänderter/gelöschter Dateien.
//
// Anmeldung ohne Passwort-Weitergabe: rclone authorize läuft auf dem NAS und wartet auf 127.0.0.1:53682.
// Der Benutzer meldet sich im eigenen Browser beim Anbieter an; der Browser landet danach auf einer
// (nicht ladbaren) Adresse http://127.0.0.1:53682/?code=…, die er in die Oberfläche kopiert. Die Oberfläche
// reicht sie lokal an rclone weiter, das den Code gegen ein Token tauscht.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var cloudKinds = map[string]string{"onedrive": "Microsoft OneDrive", "drive": "Google Drive"}
var cloudNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,30}$`)
var cloudFolderRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,60}$`)

type CloudTarget struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Created time.Time `json:"created"`
	Crypt   bool      `json:"crypt"` // Verschlüsselungs-Remote <name>_crypt eingerichtet
}

type CloudJob struct {
	ID       string   `json:"id"`
	Target   string   `json:"target"`
	Folder   string   `json:"folder"`   // Ordner beim Anbieter
	Sources  []string `json:"sources"`  // Unterordner des Backup-Ordners (= Freigaben von der Quelle)
	Encrypt  bool     `json:"encrypt"`  // über <target>_crypt
	Versions bool     `json:"versions"` // geänderte/gelöschte Dateien nach versionen/<datum>/ verschieben
	Enabled  bool     `json:"enabled"`
}

type CloudStatus struct {
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	State   string    `json:"state"` // laeuft, ok, teilweise, fehler
	Message string    `json:"message"`
}

type cloudFile struct {
	Targets []CloudTarget          `json:"targets"`
	Jobs    []CloudJob             `json:"jobs"`
	Status  map[string]CloudStatus `json:"status"`
}

type cloudState struct {
	mu      sync.Mutex // schützt Datei + Anmeldevorgang
	runMu   sync.Mutex // nur ein Upload gleichzeitig
	running string     // Job-ID während eines Laufs
	cancel  context.CancelFunc
	auth    *cloudAuth
}

type cloudAuth struct {
	kind   string
	cmd    *exec.Cmd
	cancel context.CancelFunc
	out    chan string // Token-JSON oder "" bei Fehler
}

const cloudLog = "/var/log/tnas-cloud.log"

func (app *App) cloudFilePath() string { return filepath.Join(app.cfg.StateDir, "cloud.json") }
func (app *App) rcloneConf() string    { return filepath.Join(app.cfg.StateDir, "rclone.conf") }

func (app *App) cloudLoad() (cloudFile, error) {
	var c cloudFile
	b, err := os.ReadFile(app.cloudFilePath())
	if os.IsNotExist(err) {
		c.Status = map[string]CloudStatus{}
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	if c.Status == nil {
		c.Status = map[string]CloudStatus{}
	}
	return c, err
}

func (app *App) cloudSave(c cloudFile) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return writeFileAtomic(app.cloudFilePath(), b, 0600)
}

func (app *App) setStatus(id string, st CloudStatus) {
	app.cloud.mu.Lock()
	defer app.cloud.mu.Unlock()
	c, err := app.cloudLoad()
	if err != nil {
		return
	}
	c.Status[id] = st
	_ = app.cloudSave(c)
}

// rclone startet rclone mit unserer Konfiguration (und mitgeliefertem CA-Bündel, falls vorhanden).
func (app *App) rclone(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, app.cfg.RcloneBin, append([]string{"--config", app.rcloneConf()}, args...)...)
	cmd.Env = os.Environ()
	if ca := filepath.Join(filepath.Dir(app.cfg.RcloneBin), "cacert.pem"); fileExists(ca) {
		cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+ca)
	}
	return cmd
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func (app *App) rcloneOut(timeout time.Duration, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := app.rclone(ctx, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("rclone %s: %v %s", args[0], err, strings.TrimSpace(lastLine(string(out))))
	}
	return string(out), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ---------- rclone.conf (nur eigene Abschnitte, einfaches INI) ----------

func (app *App) confSections() (map[string][][2]string, []string) {
	secs := map[string][][2]string{}
	var order []string
	b, _ := os.ReadFile(app.rcloneConf())
	cur := ""
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "[") && strings.HasSuffix(ln, "]") {
			cur = ln[1 : len(ln)-1]
			order = append(order, cur)
			secs[cur] = nil
		} else if k, v, ok := strings.Cut(ln, " = "); ok && cur != "" {
			secs[cur] = append(secs[cur], [2]string{k, v})
		}
	}
	return secs, order
}

func (app *App) confWrite(secs map[string][][2]string, order []string) error {
	var sb strings.Builder
	for _, n := range order {
		kv, ok := secs[n]
		if !ok {
			continue
		}
		fmt.Fprintf(&sb, "[%s]\n", n)
		for _, p := range kv {
			fmt.Fprintf(&sb, "%s = %s\n", p[0], p[1])
		}
		sb.WriteString("\n")
	}
	return writeFileAtomic(app.rcloneConf(), []byte(sb.String()), 0600)
}

func (app *App) confSet(name string, kv [][2]string) error {
	secs, order := app.confSections()
	if _, ok := secs[name]; !ok {
		order = append(order, name)
	}
	secs[name] = kv
	return app.confWrite(secs, order)
}

func (app *App) confDelete(names ...string) error {
	secs, order := app.confSections()
	for _, n := range names {
		delete(secs, n)
	}
	return app.confWrite(secs, order)
}

// ---------- Quellen ----------

// cloudSources: Unterordner des Backup-Ordners (das sind die gesicherten Freigaben der Quelle).
func (app *App) cloudSources() []string {
	ents, _ := os.ReadDir(app.cfg.BackupDir)
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// sourcePath: bevorzugt der neueste Schnappschuss (konsistenter Stand), sonst der Live-Ordner.
func (app *App) sourcePath(name string) string {
	snaps := snapshots(app.cfg)
	if len(snaps) > 0 {
		p := filepath.Join(app.cfg.SnapshotDir, snaps[0].Name, name)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return filepath.Join(app.cfg.BackupDir, name)
}

// ---------- API ----------

func (app *App) apiCloud(w http.ResponseWriter, r *http.Request, s *session) {
	app.cloud.mu.Lock()
	c, err := app.cloudLoad()
	authKind := ""
	if app.cloud.auth != nil {
		authKind = app.cloud.auth.kind
	}
	app.cloud.mu.Unlock()
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	ver := ""
	rcloneOK := fileExists(app.cfg.RcloneBin)
	if rcloneOK {
		if out, err := app.rcloneOut(10*time.Second, "", "version"); err == nil {
			ver = strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
		}
	}
	writeJSON(w, 200, map[string]any{
		"targets": c.Targets, "jobs": c.Jobs, "status": c.Status, "running": app.cloud.running,
		"sources": app.cloudSources(), "kinds": cloudKinds, "rclone": rcloneOK, "rclone_version": ver,
		"rclone_bin": app.cfg.RcloneBin, "max_hours": app.cfg.CloudMaxHours, "auth_pending": authKind,
		"source": app.cfg.BackupSource,
	})
}

func (app *App) apiCloudAuthStart(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ Kind string }
	if err := readBody(r, &req); err != nil || cloudKinds[req.Kind] == "" {
		writeErr(w, 400, "Unbekannter Anbieter")
		return
	}
	if !fileExists(app.cfg.RcloneBin) {
		writeErr(w, 500, "rclone fehlt (%s)", app.cfg.RcloneBin)
		return
	}
	app.cloud.mu.Lock()
	defer app.cloud.mu.Unlock()
	if app.cloud.auth != nil {
		app.cloud.auth.cancel()
		app.cloud.auth = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	cmd := app.rclone(ctx, "authorize", req.Kind, "--auth-no-open-browser")
	stderr, _ := cmd.StderrPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		cancel()
		writeErr(w, 500, "%v", err)
		return
	}
	a := &cloudAuth{kind: req.Kind, cmd: cmd, cancel: cancel, out: make(chan string, 1)}
	app.cloud.auth = a
	// stdout: Token zwischen den Markierungen einsammeln
	go func() {
		all, _ := io.ReadAll(stdout)
		_ = cmd.Wait()
		cancel()
		txt := string(all)
		tok := ""
		if i := strings.Index(txt, "{"); i >= 0 {
			if j := strings.LastIndex(txt, "}"); j > i {
				tok = txt[i : j+1]
			}
		}
		a.out <- tok
	}()
	// stderr: lokale Anmelde-Adresse finden
	localURL := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		re := regexp.MustCompile(`http://127\.0\.0\.1:53682/auth\?state=[A-Za-z0-9_-]+`)
		sent := false
		for sc.Scan() {
			if m := re.FindString(sc.Text()); m != "" && !sent {
				localURL <- m
				sent = true
			}
		}
		if !sent {
			localURL <- ""
		}
	}()
	var lu string
	select {
	case lu = <-localURL:
	case <-time.After(20 * time.Second):
	}
	if lu == "" {
		cancel()
		app.cloud.auth = nil
		writeErr(w, 500, "rclone authorize lieferte keine Anmelde-Adresse")
		return
	}
	// Die lokale Adresse leitet zum Anbieter weiter – Ziel auslesen, ohne zu folgen
	cl := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(lu)
	if err != nil {
		cancel()
		app.cloud.auth = nil
		writeErr(w, 500, "%v", err)
		return
	}
	resp.Body.Close()
	prov := resp.Header.Get("Location")
	if !strings.HasPrefix(prov, "https://") {
		cancel()
		app.cloud.auth = nil
		writeErr(w, 500, "Keine Anmeldeseite des Anbieters erhalten")
		return
	}
	app.logf("%s: Cloud-Anmeldung gestartet (%s)", s.user, req.Kind)
	writeJSON(w, 200, map[string]string{"url": prov})
}

func (app *App) apiCloudAuthFinish(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ Name, Redirect string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	if !cloudNameRe.MatchString(req.Name) || strings.HasSuffix(req.Name, "_crypt") {
		writeErr(w, 400, "Name: 2–31 Zeichen, Kleinbuchstaben/Ziffern/-/_ , mit Buchstabe beginnend")
		return
	}
	u, err := url.Parse(strings.TrimSpace(req.Redirect))
	if err != nil || (u.Host != "127.0.0.1:53682" && u.Host != "localhost:53682") || u.Query().Get("code") == "" {
		writeErr(w, 400, "Bitte die vollständige Adresse aus der Adresszeile einfügen (beginnt mit http://127.0.0.1:53682/ und enthält code=)")
		return
	}
	app.cloud.mu.Lock()
	a := app.cloud.auth
	c, _ := app.cloudLoad()
	app.cloud.mu.Unlock()
	if a == nil {
		writeErr(w, 409, "Keine laufende Anmeldung – bitte neu starten")
		return
	}
	for _, t := range c.Targets {
		if t.Name == req.Name {
			writeErr(w, 409, "Ein Ziel mit diesem Namen gibt es schon")
			return
		}
	}
	// Code lokal an rclone weiterreichen
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Get("http://127.0.0.1:53682" + u.Path + "?" + u.RawQuery)
	if err == nil {
		resp.Body.Close()
	}
	var tok string
	select {
	case tok = <-a.out:
	case <-time.After(60 * time.Second):
	}
	app.cloud.mu.Lock()
	if app.cloud.auth == a {
		app.cloud.auth = nil
	}
	app.cloud.mu.Unlock()
	a.cancel()
	if tok == "" || !json.Valid([]byte(tok)) {
		writeErr(w, 500, "Anmeldung fehlgeschlagen (kein Token erhalten) – bitte erneut versuchen")
		return
	}
	kv := [][2]string{{"type", a.kind}, {"token", compactJSON(tok)}}
	if a.kind == "drive" {
		kv = append(kv, [2]string{"scope", "drive"})
	}
	if a.kind == "onedrive" {
		id, typ, err := oneDriveInfo(tok, app)
		if err != nil {
			writeErr(w, 500, "OneDrive: Laufwerk nicht ermittelbar: %v", err)
			return
		}
		kv = append(kv, [2]string{"drive_id", id}, [2]string{"drive_type", typ})
	}
	app.cloud.mu.Lock()
	defer app.cloud.mu.Unlock()
	if err := app.confSet(req.Name, kv); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	c, _ = app.cloudLoad()
	c.Targets = append(c.Targets, CloudTarget{Name: req.Name, Kind: a.kind, Created: time.Now()})
	if err := app.cloudSave(c); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Cloud-Ziel %s (%s) eingerichtet", s.user, req.Name, a.kind)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func compactJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// oneDriveInfo fragt mit dem frischen Token das Standard-Laufwerk ab (Graph API).
func oneDriveInfo(tok string, app *App) (string, string, error) {
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(tok), &t); err != nil || t.AccessToken == "" {
		return "", "", errors.New("Token unlesbar")
	}
	req, _ := http.NewRequest("GET", "https://graph.microsoft.com/v1.0/me/drive", nil)
	req.Header.Set("Authorization", "Bearer "+t.AccessToken)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var d struct {
		ID        string `json:"id"`
		DriveType string `json:"driveType"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil || d.ID == "" {
		return "", "", fmt.Errorf("Antwort %d", resp.StatusCode)
	}
	return d.ID, d.DriveType, nil
}

func (app *App) apiCloudTargetDelete(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ Name string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	app.cloud.mu.Lock()
	defer app.cloud.mu.Unlock()
	c, _ := app.cloudLoad()
	for _, j := range c.Jobs {
		if j.Target == req.Name {
			writeErr(w, 409, "Ziel wird noch von einem Auftrag benutzt")
			return
		}
	}
	var keep []CloudTarget
	for _, t := range c.Targets {
		if t.Name != req.Name {
			keep = append(keep, t)
		}
	}
	c.Targets = keep
	_ = app.confDelete(req.Name, req.Name+"_crypt")
	if err := app.cloudSave(c); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Cloud-Ziel %s entfernt (Daten beim Anbieter bleiben erhalten)", s.user, req.Name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (app *App) apiCloudTest(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ Name string }
	if err := readBody(r, &req); err != nil || !cloudNameRe.MatchString(req.Name) {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	out, err := app.rcloneOut(60*time.Second, "", "about", req.Name+":", "--json")
	if err != nil {
		writeErr(w, 502, "%v", err)
		return
	}
	var about map[string]any
	_ = json.Unmarshal([]byte(out), &about)
	writeJSON(w, 200, about)
}

func (app *App) apiCloudJobSave(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Job           CloudJob
		CryptPassword string
	}
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	j := req.Job
	j.Folder = strings.TrimSpace(j.Folder)
	if !cloudFolderRe.MatchString(j.Folder) {
		writeErr(w, 400, "Ordnername beim Anbieter: Buchstaben, Ziffern, Leerzeichen, . _ -")
		return
	}
	valid := map[string]bool{}
	for _, n := range app.cloudSources() {
		valid[n] = true
	}
	if len(j.Sources) == 0 {
		writeErr(w, 400, "Mindestens eine Freigabe auswählen")
		return
	}
	for _, n := range j.Sources {
		if !valid[n] {
			writeErr(w, 400, "Unbekannte Freigabe: %s", n)
			return
		}
	}
	app.cloud.mu.Lock()
	defer app.cloud.mu.Unlock()
	c, _ := app.cloudLoad()
	ti := -1
	for i, t := range c.Targets {
		if t.Name == j.Target {
			ti = i
		}
	}
	if ti < 0 {
		writeErr(w, 400, "Unbekanntes Ziel")
		return
	}
	if j.Encrypt && !c.Targets[ti].Crypt {
		if len(req.CryptPassword) < 12 {
			writeErr(w, 400, "Für die Verschlüsselung ein Passwort mit mindestens 12 Zeichen angeben")
			return
		}
		ob, err := app.rcloneOut(20*time.Second, req.CryptPassword+"\n", "obscure", "-")
		if err != nil {
			writeErr(w, 500, "%v", err)
			return
		}
		if err := app.confSet(j.Target+"_crypt", [][2]string{{"type", "crypt"}, {"remote", j.Target + ":TNAS-verschluesselt"},
			{"password", strings.TrimSpace(ob)}, {"filename_encryption", "standard"}, {"directory_name_encryption", "true"}}); err != nil {
			writeErr(w, 500, "%v", err)
			return
		}
		c.Targets[ti].Crypt = true
		app.logf("%s: Verschlüsselung für Cloud-Ziel %s eingerichtet", s.user, j.Target)
	}
	if j.ID == "" {
		j.ID = fmt.Sprintf("j%d", time.Now().Unix())
		c.Jobs = append(c.Jobs, j)
	} else {
		found := false
		for i := range c.Jobs {
			if c.Jobs[i].ID == j.ID {
				c.Jobs[i] = j
				found = true
			}
		}
		if !found {
			writeErr(w, 404, "Auftrag nicht gefunden")
			return
		}
	}
	if err := app.cloudSave(c); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Cloud-Auftrag %s gespeichert (%s → %s:%s)", s.user, j.ID, strings.Join(j.Sources, ","), j.Target, j.Folder)
	writeJSON(w, 200, map[string]string{"id": j.ID})
}

func (app *App) apiCloudJobDelete(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ ID string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	app.cloud.mu.Lock()
	defer app.cloud.mu.Unlock()
	c, _ := app.cloudLoad()
	var keep []CloudJob
	for _, j := range c.Jobs {
		if j.ID != req.ID {
			keep = append(keep, j)
		}
	}
	c.Jobs = keep
	delete(c.Status, req.ID)
	if err := app.cloudSave(c); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Cloud-Auftrag %s gelöscht (Daten beim Anbieter bleiben erhalten)", s.user, req.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (app *App) apiCloudRun(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		ID   string
		Stop bool
	}
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	if req.Stop {
		app.cloud.mu.Lock()
		if app.cloud.cancel != nil {
			app.cloud.cancel()
		}
		app.cloud.mu.Unlock()
		app.logf("%s: Cloud-Upload abgebrochen", s.user)
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if !app.cloud.runMu.TryLock() {
		writeErr(w, 409, "Es läuft bereits ein Upload")
		return
	}
	app.logf("%s: Cloud-Upload von Hand gestartet (%s)", s.user, map[bool]string{true: "alle", false: req.ID}[req.ID == ""])
	go func() {
		defer app.cloud.runMu.Unlock()
		app.cloudRunJobs(req.ID, time.Duration(app.cfg.CloudMaxHours)*time.Hour)
	}()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- Ausführung ----------

// cloudRunJobs führt den Auftrag id (oder alle aktiven, wenn id leer) aus. Rückgabe: Anzahl Fehler.
func (app *App) cloudRunJobs(id string, maxDur time.Duration) int {
	app.cloud.mu.Lock()
	c, err := app.cloudLoad()
	app.cloud.mu.Unlock()
	if err != nil {
		app.logf("Cloud: %v", err)
		return 1
	}
	deadline := time.Now().Add(maxDur)
	fails := 0
	for _, j := range c.Jobs {
		if (id == "" && !j.Enabled) || (id != "" && j.ID != id) {
			continue
		}
		left := time.Until(deadline)
		if left < time.Minute {
			app.setStatus(j.ID, CloudStatus{Start: time.Now(), End: time.Now(), State: "teilweise", Message: "Zeitfenster aufgebraucht – nächstes Mal"})
			continue
		}
		if err := app.cloudRunJob(j, left); err != nil {
			fails++
		}
	}
	return fails
}

func (app *App) cloudRunJob(j CloudJob, maxDur time.Duration) error {
	ctx, cancel := context.WithCancel(context.Background())
	app.cloud.mu.Lock()
	app.cloud.running, app.cloud.cancel = j.ID, cancel
	app.cloud.mu.Unlock()
	defer func() {
		cancel()
		app.cloud.mu.Lock()
		app.cloud.running, app.cloud.cancel = "", nil
		app.cloud.mu.Unlock()
	}()
	start := time.Now()
	app.setStatus(j.ID, CloudStatus{Start: start, State: "laeuft", Message: "Upload läuft …"})
	remote := j.Target
	if j.Encrypt {
		remote += "_crypt"
	}
	base := remote + ":" + j.Folder
	day := time.Now().Format("2006-01-02")
	state, msgs := "ok", []string{}
	for _, src := range j.Sources {
		left := maxDur - time.Since(start)
		if left < time.Minute {
			state = "teilweise"
			msgs = append(msgs, src+": Zeitfenster aufgebraucht")
			break
		}
		args := []string{"sync", app.sourcePath(src), base + "/aktuell/" + src,
			"--log-file", cloudLog, "--log-level", "NOTICE", "--stats", "10m", "--stats-log-level", "NOTICE",
			"--transfers", "4", "--checkers", "8", "--max-duration", fmt.Sprintf("%ds", int(left.Seconds())), "--cutoff-mode", "soft",
			"--exclude", "#recycle/**", "--exclude", ".recycle/**", "--exclude", ".snapshots/**", "--retries", "3", "--low-level-retries", "10"}
		if j.Versions {
			args = append(args, "--backup-dir", base+"/versionen/"+day+"/"+src)
		}
		appendCloudLog(fmt.Sprintf("=== %s Auftrag %s: %s -> %s/aktuell/%s", time.Now().Format("2006-01-02 15:04:05"), j.ID, src, base, src))
		cmd := app.rclone(ctx, args...)
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		switch {
		case code == 0:
			msgs = append(msgs, src+": ok")
		case code == 10 || code == 8: // Zeit-/Mengengrenze erreicht – wird beim nächsten Lauf fortgesetzt
			if state == "ok" {
				state = "teilweise"
			}
			msgs = append(msgs, src+": Zeitfenster erreicht, wird fortgesetzt")
		case ctx.Err() != nil:
			state = "fehler"
			msgs = append(msgs, src+": abgebrochen")
		default:
			state = "fehler"
			msgs = append(msgs, fmt.Sprintf("%s: Fehler (Exit %d) %s", src, code, strings.TrimSpace(lastLine(string(out)))))
		}
		if ctx.Err() != nil {
			break
		}
	}
	msg := strings.Join(msgs, "; ")
	app.setStatus(j.ID, CloudStatus{Start: start, End: time.Now(), State: state, Message: msg})
	app.logf("Cloud-Auftrag %s: %s (%s)", j.ID, state, msg)
	appendCloudLog(fmt.Sprintf("=== %s Auftrag %s: %s – %s", time.Now().Format("2006-01-02 15:04:05"), j.ID, state, msg))
	if state == "fehler" {
		return errors.New(msg)
	}
	return nil
}

func appendCloudLog(line string) {
	f, err := os.OpenFile(cloudLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// cloudRunCLI: Aufruf "tnas-backup-gui -cloud-run" aus dem nächtlichen Backup-Skript der Quelle,
// nachdem die Schnappschüsse angelegt sind. Läuft höchstens cloud_max_hours.
func (app *App) cloudRunCLI() int {
	lock := filepath.Join(app.cfg.StateDir, "cloud-run.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if fi, e := os.Stat(lock); e == nil && time.Since(fi.ModTime()) > 24*time.Hour {
			_ = os.Remove(lock)
			f, err = os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "Cloud-Upload läuft bereits")
			return 2
		}
	}
	f.Close()
	defer os.Remove(lock)
	c, err := app.cloudLoad()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	n := 0
	for _, j := range c.Jobs {
		if j.Enabled {
			n++
		}
	}
	if n == 0 {
		fmt.Println("Cloud-Backup: keine aktiven Aufträge")
		return 0
	}
	fails := app.cloudRunJobs("", time.Duration(app.cfg.CloudMaxHours)*time.Hour)
	c, _ = app.cloudLoad()
	for _, j := range c.Jobs {
		if st, ok := c.Status[j.ID]; ok && j.Enabled {
			fmt.Printf("Cloud-Auftrag %s (%s:%s): %s – %s\n", j.ID, j.Target, j.Folder, st.State, st.Message)
		}
	}
	if fails > 0 {
		return 1
	}
	return 0
}
