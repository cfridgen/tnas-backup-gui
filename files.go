package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Dateibrowser: nur lesend. Wurzeln sind Freigaben ("share:<Name>") oder ein Freigaben-Ordner
// in einer Backup-Version ("snap:<Schnappschuss>/<Ordner>"). Alle Pfade werden nach dem Auflösen
// von Symlinks geprüft, damit nichts außerhalb der Wurzel erreichbar ist.

type fileEntry struct {
	Name  string    `json:"name"`
	Dir   bool      `json:"dir"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
	Path  string    `json:"path,omitempty"` // relativ zur Wurzel (Suche)
}

var errOutside = errors.New("Pfad außerhalb der Freigabe")

// resolveRoot liefert das Wurzelverzeichnis zu einer Wurzel-Kennung.
func (app *App) resolveRoot(root string) (string, error) {
	switch {
	case strings.HasPrefix(root, "share:"):
		name := strings.TrimPrefix(root, "share:")
		app.shares.mu.Lock()
		list, err := app.shares.load()
		app.shares.mu.Unlock()
		if err != nil {
			return "", err
		}
		for _, s := range list {
			if s.Name == name && s.System == "" {
				return s.Path, nil
			}
		}
		return "", errors.New("Freigabe nicht gefunden")
	case strings.HasPrefix(root, "snap:"):
		v := strings.TrimPrefix(root, "snap:")
		snap, sub, ok := strings.Cut(v, "/")
		if !ok || !snapRe.MatchString(snap) || sub == "" || strings.ContainsAny(sub, "/\\") || sub == ".." || sub == "." {
			return "", errors.New("Ungültige Version")
		}
		return filepath.Join(app.cfg.SnapshotDir, snap, sub), nil
	}
	return "", errors.New("Ungültige Wurzel")
}

// resolve verbindet Wurzel und relativen Pfad und prüft, dass das Ziel (auch über Symlinks) darin liegt.
func (app *App) resolve(root, rel string) (base, abs string, err error) {
	base, err = app.resolveRoot(root)
	if err != nil {
		return
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", "", errors.New("Ordner nicht vorhanden")
	}
	abs = filepath.Join(realBase, filepath.Clean("/"+rel))
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", errors.New("Nicht gefunden")
	}
	if real != realBase && !strings.HasPrefix(real, realBase+"/") {
		return "", "", errOutside
	}
	return realBase, real, nil
}

func (app *App) apiFileRoots(w http.ResponseWriter, r *http.Request, s *session) {
	app.shares.mu.Lock()
	list, _ := app.shares.load()
	app.shares.mu.Unlock()
	type sh struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	var shares []sh
	for _, x := range list {
		if x.System == "" {
			shares = append(shares, sh{x.Name, x.Path})
		}
	}
	writeJSON(w, 200, map[string]any{"shares": shares, "snapshots": snapshots(app.cfg)})
}

func (app *App) apiFileList(w http.ResponseWriter, r *http.Request, s *session) {
	q := r.URL.Query()
	_, abs, err := app.resolve(q.Get("root"), q.Get("path"))
	if err != nil {
		writeErr(w, 404, "%v", err)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	defer f.Close()
	const max = 5000
	ents, err := f.ReadDir(max + 1)
	if err != nil && err != io.EOF {
		writeErr(w, 400, "Kein Ordner")
		return
	}
	truncated := len(ents) > max
	if truncated {
		ents = ents[:max]
	}
	res := make([]fileEntry, 0, len(ents))
	for _, e := range ents {
		if e.Name() == "#recycle" || e.Name() == ".snapshots" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fe := fileEntry{Name: e.Name(), Dir: info.IsDir(), Size: info.Size(), MTime: info.ModTime()}
		if info.Mode()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(abs, e.Name())); err == nil {
				fe.Dir, fe.Size = st.IsDir(), st.Size()
			}
		}
		res = append(res, fe)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Dir != res[j].Dir {
			return res[i].Dir
		}
		return strings.ToLower(res[i].Name) < strings.ToLower(res[j].Name)
	})
	writeJSON(w, 200, map[string]any{"entries": res, "truncated": truncated})
}

func (app *App) apiFileSearch(w http.ResponseWriter, r *http.Request, s *session) {
	q := r.URL.Query()
	base, abs, err := app.resolve(q.Get("root"), q.Get("path"))
	if err != nil {
		writeErr(w, 404, "%v", err)
		return
	}
	needle := strings.ToLower(strings.TrimSpace(q.Get("q")))
	if len(needle) < 2 {
		writeErr(w, 400, "Bitte mindestens 2 Zeichen eingeben")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	const max = 500
	var res []fileEntry
	timedOut := false
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			timedOut = true
			return filepath.SkipAll
		}
		if d.IsDir() && (d.Name() == "#recycle" || d.Name() == ".snapshots") {
			return filepath.SkipDir
		}
		if p != abs && strings.Contains(strings.ToLower(d.Name()), needle) {
			info, err := d.Info()
			if err == nil {
				rel, _ := filepath.Rel(base, p)
				res = append(res, fileEntry{Name: d.Name(), Dir: d.IsDir(), Size: info.Size(), MTime: info.ModTime(), Path: rel})
			}
			if len(res) >= max {
				return filepath.SkipAll
			}
		}
		return nil
	})
	writeJSON(w, 200, map[string]any{"results": res, "truncated": len(res) >= max, "timeout": timedOut})
}

// ---------- Anzeige ----------

const viewMax = 512 << 10

// Medientypen fest vorgegeben – die Mime-Tabellen auf dem NAS sind unvollständig (".ts" = Qt-Übersetzung …)
var mediaTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm", ".mkv": "video/x-matroska",
	".avi": "video/x-msvideo", ".divx": "video/x-msvideo", ".mpg": "video/mpeg", ".mpeg": "video/mpeg", ".vob": "video/mpeg",
	".ts": "video/mp2t", ".m2ts": "video/mp2t", ".mts": "video/mp2t", ".wmv": "video/x-ms-wmv", ".flv": "video/x-flv", ".3gp": "video/3gpp",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac", ".ogg": "audio/ogg", ".oga": "audio/ogg",
	".opus": "audio/ogg", ".wav": "audio/wav", ".wma": "audio/x-ms-wma", ".aiff": "audio/aiff", ".aif": "audio/aiff",
}

func fileKind(name string, head []byte) (kind, mimeType string) {
	ext := strings.ToLower(filepath.Ext(name))
	if mt, ok := mediaTypes[ext]; ok {
		return strings.SplitN(mt, "/", 2)[0], mt
	}
	mimeType = mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = http.DetectContentType(head)
	}
	switch {
	case ext == ".svg":
		return "text", "text/plain"
	case strings.HasPrefix(mimeType, "image/"):
		return "image", mimeType
	case strings.HasPrefix(mimeType, "audio/"):
		return "audio", mimeType
	case strings.HasPrefix(mimeType, "video/"):
		return "video", mimeType
	case mimeType == "application/pdf":
		return "pdf", mimeType
	}
	if isText(head) {
		return "text", "text/plain"
	}
	return "binary", "application/octet-stream"
}

func isText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	ctrl := 0
	for _, c := range b {
		if c < 9 || (c > 13 && c < 32) {
			ctrl++
		}
	}
	return ctrl*100/len(b) < 2
}

// latin1 wandelt Text in ISO-8859-1/Windows-1252 nach UTF-8 (alte Windows-Dateien).
func latin1(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

func (app *App) apiFileView(w http.ResponseWriter, r *http.Request, s *session) {
	q := r.URL.Query()
	_, abs, err := app.resolve(q.Get("root"), q.Get("path"))
	if err != nil {
		writeErr(w, 404, "%v", err)
		return
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		writeErr(w, 400, "Keine Datei")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	defer f.Close()
	head := make([]byte, viewMax)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	kind, mt := fileKind(abs, head[:min(n, 8192)])
	res := map[string]any{"name": filepath.Base(abs), "size": st.Size(), "mtime": st.ModTime(), "kind": kind, "mime": mt}
	switch kind {
	case "text":
		// am Ende evtl. abgeschnittenes UTF-8-Zeichen nicht als Fehler werten
		probe := head
		for i := 0; i < 3 && len(probe) > 0 && !utf8.Valid(probe); i++ {
			probe = probe[:len(probe)-1]
		}
		if utf8.Valid(probe) {
			res["text"], res["encoding"] = string(probe), "UTF-8"
		} else {
			res["text"], res["encoding"] = latin1(head), "ISO-8859-1"
		}
		res["truncated"] = st.Size() > int64(n)
	case "binary":
		h := head[:min(n, 4096)]
		res["hex"] = hex.Dump(h)
		res["truncated"] = st.Size() > int64(len(h))
	}
	writeJSON(w, 200, res)
}

// apiFileRaw liefert den Dateiinhalt (Bilder/Audio/Video/PDF zur Anzeige, sonst als Download).
func (app *App) apiFileRaw(w http.ResponseWriter, r *http.Request, s *session) {
	q := r.URL.Query()
	_, abs, err := app.resolve(q.Get("root"), q.Get("path"))
	if err != nil {
		writeErr(w, 404, "%v", err)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		writeErr(w, 404, "%v", err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeErr(w, 400, "Keine Datei")
		return
	}
	head := make([]byte, 8192)
	n, _ := io.ReadFull(f, head)
	kind, mt := fileKind(abs, head[:n])
	name := filepath.Base(abs)
	inline := q.Get("download") == "" && (kind == "image" || kind == "audio" || kind == "video" || kind == "pdf")
	if inline {
		w.Header().Set("Content-Type", mt)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	if kind != "pdf" {
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'")
	} else {
		w.Header().Del("Content-Security-Policy") // sonst blockiert Chrome seinen PDF-Betrachter
	}
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	app.logf("%s: Datei gelesen: %s", s.user, abs)
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// ---------- Versionen einer Datei ----------

type fileVersion struct {
	Root    string    `json:"root"`
	Path    string    `json:"path"`
	Label   string    `json:"label"`
	Snap    string    `json:"snap,omitempty"`
	Time    time.Time `json:"time,omitempty"`
	Exists  bool      `json:"exists"`
	Size    int64     `json:"size"`
	MTime   time.Time `json:"mtime"`
	Current bool      `json:"current"`
}

// backupRel bildet eine Wurzel+Pfad auf "<Freigaben-Ordner>/<Pfad>" relativ zum Backup-Ordner ab.
func (app *App) backupRel(root, rel string) (string, bool) {
	if strings.HasPrefix(root, "snap:") {
		_, sub, _ := strings.Cut(strings.TrimPrefix(root, "snap:"), "/")
		return filepath.Join(sub, filepath.Clean("/"+rel)), true
	}
	base, err := app.resolveRoot(root)
	if err != nil {
		return "", false
	}
	abs := filepath.Join(base, filepath.Clean("/"+rel))
	if !insideDir(app.cfg.BackupDir, abs) {
		return "", false
	}
	r, err := filepath.Rel(app.cfg.BackupDir, abs)
	return r, err == nil
}

// liveRoot sucht die Freigabe, deren Pfad <BackupDir>/<Ordner> ist (für die aktuelle Version).
func (app *App) liveRoot(sub string) (string, bool) {
	app.shares.mu.Lock()
	list, _ := app.shares.load()
	app.shares.mu.Unlock()
	want := filepath.Join(app.cfg.BackupDir, sub)
	for _, s := range list {
		if filepath.Clean(s.Path) == want && s.System == "" {
			return "share:" + s.Name, true
		}
	}
	return "", false
}

func (app *App) apiFileHistory(w http.ResponseWriter, r *http.Request, s *session) {
	q := r.URL.Query()
	rel, ok := app.backupRel(q.Get("root"), q.Get("path"))
	if !ok || strings.HasPrefix(rel, "..") {
		writeJSON(w, 200, map[string]any{"versions": []fileVersion{}, "note": "Für diese Datei gibt es keine Backup-Versionen (liegt nicht im Backup-Ordner)."})
		return
	}
	sub, inner, _ := strings.Cut(rel, "/")
	var res []fileVersion
	if lr, ok := app.liveRoot(sub); ok {
		v := fileVersion{Root: lr, Path: inner, Label: "Aktuell", Current: true}
		if st, err := os.Stat(filepath.Join(app.cfg.BackupDir, rel)); err == nil && !st.IsDir() {
			v.Exists, v.Size, v.MTime = true, st.Size(), st.ModTime()
		}
		res = append(res, v)
	}
	for _, sn := range snapshots(app.cfg) {
		v := fileVersion{Root: "snap:" + sn.Name + "/" + sub, Path: inner, Snap: sn.Name, Time: sn.Time}
		if st, err := os.Stat(filepath.Join(app.cfg.SnapshotDir, sn.Name, rel)); err == nil && !st.IsDir() {
			v.Exists, v.Size, v.MTime = true, st.Size(), st.ModTime()
		}
		res = append(res, v)
	}
	writeJSON(w, 200, map[string]any{"versions": res})
}
