package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Befristete Stream-Links für externe Player (VLC, IINA …), die das Sitzungs-Cookie nicht kennen.
// Ein Link gilt nur für eine Datei, nur von der IP, die ihn angefordert hat, und 12 Stunden.

const streamTTL = 12 * time.Hour

type streamTok struct {
	path    string
	ip      string
	expires time.Time
}

var streams = struct {
	sync.Mutex
	m map[string]streamTok
}{m: map[string]streamTok{}}

func (app *App) newStreamURL(r *http.Request, root, rel string) (string, string, error) {
	_, abs, err := app.resolve(root, rel)
	if err != nil {
		return "", "", err
	}
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		return "", "", fmt.Errorf("Keine Datei")
	}
	tok := randHex(24)
	streams.Lock()
	for k, v := range streams.m { // Abgelaufene aufräumen
		if time.Now().After(v.expires) {
			delete(streams.m, k)
		}
	}
	streams.m[tok] = streamTok{path: abs, ip: clientIP(r), expires: time.Now().Add(streamTTL)}
	streams.Unlock()
	name := filepath.Base(abs)
	u := "http://" + r.Host + "/stream/" + tok + "/" + url.PathEscape(name)
	return u, name, nil
}

func (app *App) apiStreamLink(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ Root, Path string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	u, _, err := app.newStreamURL(r, req.Root, req.Path)
	if err != nil {
		writeErr(w, 404, "%v", err)
		return
	}
	app.logf("%s: Stream-Link erzeugt: %s/%s", s.user, req.Root, req.Path)
	writeJSON(w, 200, map[string]string{"url": u})
}

// apiStreamM3U liefert eine Abspielliste – das Betriebssystem öffnet sie mit dem Standard-Videoplayer.
func (app *App) apiStreamM3U(w http.ResponseWriter, r *http.Request, s *session) {
	q := r.URL.Query()
	u, name, err := app.newStreamURL(r, q.Get("root"), q.Get("path"))
	if err != nil {
		writeErr(w, 404, "%v", err)
		return
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.Map(func(c rune) rune {
		if c == '"' || c == '\\' || c < 32 {
			return '_'
		}
		return c
	}, base)+`.m3u"`)
	app.logf("%s: Abspielliste erzeugt: %s", s.user, name)
	fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1,%s\n%s\n", strings.ReplaceAll(name, "\n", " "), u)
}

// handleStream liefert die Datei an den Player (mit Range-Unterstützung zum Spulen).
func (app *App) handleStream(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/stream/"), "/", 2)
	streams.Lock()
	t, ok := streams.m[parts[0]]
	streams.Unlock()
	if !ok || time.Now().After(t.expires) || t.ip != clientIP(r) {
		http.Error(w, "Link ungültig oder abgelaufen", http.StatusForbidden)
		return
	}
	f, err := os.Open(t.path)
	if err != nil {
		http.Error(w, "Datei nicht gefunden", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "Fehler", 500)
		return
	}
	kind, mt := fileKind(t.path, nil)
	if kind != "video" && kind != "audio" {
		mt = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	http.ServeContent(w, r, filepath.Base(t.path), st.ModTime(), f)
}
