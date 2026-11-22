package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Automatische Anmeldung („Auf diesem Gerät angemeldet bleiben“):
// Der Browser bekommt ein zufälliges Geräte-Token als Cookie (1 Jahr). Gespeichert wird nur dessen
// SHA-256-Fingerabdruck – zusammen mit Benutzer und IP. Gültig nur von derselben IP-Adresse,
// solange der Benutzer Administrator ist. Abmelden oder Passwortwechsel machen es ungültig.

const deviceCookie = "tbg_device"
const deviceDays = 365

type device struct {
	Hash     string    `json:"hash"`
	User     string    `json:"user"`
	IP       string    `json:"ip"`
	Agent    string    `json:"agent"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used"`
}

var devMu sync.Mutex

func (a *authState) deviceFile() string { return filepath.Join(a.cfg.StateDir, "devices.json") }

func (a *authState) loadDevices() []device {
	var list []device
	if b, err := os.ReadFile(a.deviceFile()); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	// abgelaufene Einträge verwerfen
	var keep []device
	for _, d := range list {
		if time.Since(d.LastUsed) < deviceDays*24*time.Hour {
			keep = append(keep, d)
		}
	}
	return keep
}

func (a *authState) saveDevices(list []device) {
	b, _ := json.MarshalIndent(list, "", "  ")
	_ = writeFileAtomic(a.deviceFile(), b, 0600)
}

func tokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// rememberDevice legt ein Geräte-Token an und setzt das Cookie.
func (a *authState) rememberDevice(w http.ResponseWriter, r *http.Request, user string) {
	tok := randHex(32)
	devMu.Lock()
	list := a.loadDevices()
	list = append(list, device{Hash: tokenHash(tok), User: user, IP: clientIP(r), Agent: trunc(r.UserAgent(), 200),
		Created: time.Now(), LastUsed: time.Now()})
	a.saveDevices(list)
	devMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: deviceDays * 86400})
}

// autoLogin prüft das Geräte-Cookie und legt bei Erfolg eine neue Sitzung an.
func (a *authState) autoLogin(w http.ResponseWriter, r *http.Request) *session {
	c, err := r.Cookie(deviceCookie)
	if err != nil || len(c.Value) != 64 {
		return nil
	}
	h := tokenHash(c.Value)
	ip := clientIP(r)
	devMu.Lock()
	defer devMu.Unlock()
	list := a.loadDevices()
	for i, d := range list {
		if d.Hash != h {
			continue
		}
		if d.IP != ip || !a.isAdmin(d.User) {
			return nil
		}
		if hs, err := shadowHash(d.User); err != nil || len(hs) < 2 || hs[0] != '$' {
			return nil // Konto gelöscht oder gesperrt
		}
		list[i].LastUsed = time.Now()
		a.saveDevices(list)
		a.logf("Automatische Anmeldung: %s von %s", d.User, ip)
		return a.newSession(w, d.User)
	}
	return nil
}

// forgetDevice löscht das Token des aktuellen Browsers.
func (a *authState) forgetDevice(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(deviceCookie); err == nil {
		h := tokenHash(c.Value)
		devMu.Lock()
		var keep []device
		for _, d := range a.loadDevices() {
			if d.Hash != h {
				keep = append(keep, d)
			}
		}
		a.saveDevices(keep)
		devMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: deviceCookie, Value: "", Path: "/", MaxAge: -1})
}

// revokeUserDevices entfernt alle Geräte eines Benutzers außer dem des aktuellen Browsers.
func (a *authState) revokeUserDevices(user string, r *http.Request) {
	keepHash := ""
	if c, err := r.Cookie(deviceCookie); err == nil {
		keepHash = tokenHash(c.Value)
	}
	devMu.Lock()
	defer devMu.Unlock()
	var keep []device
	for _, d := range a.loadDevices() {
		if d.User != user || d.Hash == keepHash {
			keep = append(keep, d)
		}
	}
	a.saveDevices(keep)
}

func (app *App) apiDevices(w http.ResponseWriter, r *http.Request, s *session) {
	cur := ""
	if c, err := r.Cookie(deviceCookie); err == nil {
		cur = tokenHash(c.Value)
	}
	devMu.Lock()
	list := app.auth.loadDevices()
	devMu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].LastUsed.After(list[j].LastUsed) })
	type row struct {
		ID       string    `json:"id"`
		User     string    `json:"user"`
		IP       string    `json:"ip"`
		Agent    string    `json:"agent"`
		Created  time.Time `json:"created"`
		LastUsed time.Time `json:"last_used"`
		Current  bool      `json:"current"`
	}
	res := []row{}
	for _, d := range list {
		res = append(res, row{d.Hash[:16], d.User, d.IP, d.Agent, d.Created, d.LastUsed, d.Hash == cur})
	}
	writeJSON(w, 200, map[string]any{"devices": res})
}

func (app *App) apiDeviceRevoke(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ ID string }
	if err := readBody(r, &req); err != nil || len(req.ID) != 16 {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	devMu.Lock()
	var keep []device
	removed := ""
	for _, d := range app.auth.loadDevices() {
		if d.Hash[:16] == req.ID {
			removed = d.User + " / " + d.IP
			continue
		}
		keep = append(keep, d)
	}
	app.auth.saveDevices(keep)
	devMu.Unlock()
	if removed == "" {
		writeErr(w, 404, "Gerät nicht gefunden")
		return
	}
	app.logf("%s: gemerktes Gerät entfernt: %s", s.user, removed)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
