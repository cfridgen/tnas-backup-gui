package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GehirnInc/crypt"
	_ "github.com/GehirnInc/crypt/md5_crypt"
	_ "github.com/GehirnInc/crypt/sha256_crypt"
	"github.com/GehirnInc/crypt/sha512_crypt"
)

var shadowPath = "/etc/shadow" // in Tests ersetzbar

type session struct {
	user    string
	csrf    string
	expires time.Time
}

type authState struct {
	mu       sync.Mutex
	sessions map[string]*session
	fails    map[string][]time.Time // IP -> Fehlversuche
	cfg      *Config
	logf     func(format string, a ...any)
}

// newSession legt eine Sitzung an und setzt das Sitzungs-Cookie.
func (a *authState) newSession(w http.ResponseWriter, user string) *session {
	s := &session{user: user, csrf: randHex(16), expires: time.Now().Add(time.Duration(a.cfg.SessionHours) * time.Hour)}
	id := randHex(32)
	a.mu.Lock()
	a.sessions[id] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "tbg_session", Value: id, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Expires: s.expires})
	return s
}

func newAuth(cfg *Config) *authState {
	return &authState{sessions: map[string]*session{}, fails: map[string][]time.Time{}, cfg: cfg}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- Admin-Liste (eigene Datei, beim ersten Start aus TOS übernommen) ----------

func (a *authState) adminFile() string { return filepath.Join(a.cfg.StateDir, "admins.json") }

func (a *authState) admins() []string {
	var list []string
	if b, err := os.ReadFile(a.adminFile()); err == nil {
		_ = json.Unmarshal(b, &list)
		return list
	}
	// Erststart: Mitglieder der TOS-Gruppe @admin bzw. der Unix-Gruppe übernehmen
	list = tosAdmins(a.cfg)
	for _, u := range unixGroupMembers(a.cfg.AdminGroup) {
		if !contains(list, u) {
			list = append(list, u)
		}
	}
	sort.Strings(list)
	_ = a.saveAdmins(list)
	return list
}

func (a *authState) saveAdmins(list []string) error {
	sort.Strings(list)
	b, _ := json.MarshalIndent(list, "", "  ")
	return writeFileAtomic(a.adminFile(), b, 0600)
}

func (a *authState) isAdmin(user string) bool { return contains(a.admins(), user) }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func unixGroupMembers(group string) []string {
	b, err := os.ReadFile("/etc/group")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 4 && f[0] == group && f[3] != "" {
			return strings.Split(f[3], ",")
		}
	}
	return nil
}

// tosAdmins liest die TOS-Gruppe @admin aus nasdb (Benutzernamen sind dort Base64-kodiert).
func tosAdmins(cfg *Config) []string {
	if cfg.TosNasDB == "" {
		return nil
	}
	if _, err := os.Stat(cfg.TosNasDB); err != nil {
		return nil
	}
	g := base64.StdEncoding.EncodeToString([]byte("@admin"))
	out, err := run(10*time.Second, "", "sqlite3", cfg.TosNasDB, "select username from group_users where groupname='"+g+"'")
	if err != nil {
		return nil
	}
	var res []string
	for _, l := range strings.Fields(out) {
		if d, err := base64.StdEncoding.DecodeString(l); err == nil && !strings.HasPrefix(string(d), "@") {
			res = append(res, string(d))
		}
	}
	return res
}

// ---------- Passwörter ----------

func shadowHash(user string) (string, error) {
	b, err := os.ReadFile(shadowPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.SplitN(line, ":", 3)
		if len(f) >= 2 && f[0] == user {
			return f[1], nil
		}
	}
	return "", errors.New("unbekannt")
}

func checkPassword(user, pw string) bool {
	h, err := shadowHash(user)
	if err != nil || !strings.HasPrefix(h, "$") { // nur $1$/$5$/$6$, keine gesperrten Konten
		return false
	}
	if !crypt.IsHashSupported(h) {
		return false
	}
	return crypt.NewFromHash(h).Verify(h, []byte(pw)) == nil
}

func sha512Hash(pw string) (string, error) {
	salt := make([]byte, 12)
	_, _ = rand.Read(salt)
	s := "$6$" + strings.NewReplacer("+", ".", "=", "").Replace(base64.StdEncoding.EncodeToString(salt))
	return sha512_crypt.New().Generate([]byte(pw), []byte(s))
}

// setShadowPassword ersetzt den Hash in /etc/shadow atomar.
func setShadowPassword(user, hash string) error {
	b, err := os.ReadFile(shadowPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	found := false
	days := time.Now().Unix() / 86400
	for i, line := range lines {
		f := strings.Split(line, ":")
		if len(f) >= 3 && f[0] == user {
			f[1] = hash
			f[2] = itoa(int(days))
			lines[i] = strings.Join(f, ":")
			found = true
		}
	}
	if !found {
		return errors.New("Benutzer nicht in /etc/shadow")
	}
	return writeFileAtomic(shadowPath, []byte(strings.Join(lines, "\n")), 0600)
}

// ---------- Sitzungen ----------

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (a *authState) tooManyFails(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cut := time.Now().Add(-10 * time.Minute)
	var keep []time.Time
	for _, t := range a.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	a.fails[ip] = keep
	return len(keep) >= 5
}

func (a *authState) addFail(ip string) {
	a.mu.Lock()
	a.fails[ip] = append(a.fails[ip], time.Now())
	a.mu.Unlock()
}

func (a *authState) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User, Password string
		Remember       bool
	}
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	ip := clientIP(r)
	if a.tooManyFails(ip) {
		writeErr(w, 429, "Zu viele Fehlversuche – bitte 10 Minuten warten.")
		return
	}
	time.Sleep(300 * time.Millisecond) // bremst Raten zusätzlich
	if !a.isAdmin(req.User) || !checkPassword(req.User, req.Password) {
		a.addFail(ip)
		writeErr(w, 401, "Benutzername oder Passwort falsch (oder kein Administrator).")
		return
	}
	s := a.newSession(w, req.User)
	if req.Remember {
		a.rememberDevice(w, r, req.User)
	}
	a.logf("Anmeldung: %s von %s (Gerät merken: %v)", req.User, ip, req.Remember)
	writeJSON(w, 200, map[string]string{"user": s.user, "csrf": s.csrf})
}

func (a *authState) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("tbg_session"); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "tbg_session", Value: "", Path: "/", MaxAge: -1})
	a.forgetDevice(w, r) // „Abmelden“ heißt auch: dieses Gerät nicht mehr automatisch anmelden
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *authState) current(r *http.Request) *session {
	c, err := r.Cookie("tbg_session")
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[c.Value]
	if s == nil || time.Now().After(s.expires) {
		delete(a.sessions, c.Value)
		return nil
	}
	return s
}

// require schützt API-Aufrufe: gültige Sitzung + bei ändernden Aufrufen CSRF-Header.
func (a *authState) require(h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := a.current(r)
		if s == nil {
			writeErr(w, 401, "Nicht angemeldet")
			return
		}
		if r.Method != http.MethodGet {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.csrf)) != 1 {
				writeErr(w, 403, "Sicherheitsprüfung fehlgeschlagen – Seite neu laden.")
				return
			}
		}
		h(w, r, s)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
