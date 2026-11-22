package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var userNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// smbUsers liefert alle Samba-Benutzer außer "guest".
func smbUsers() []string {
	out, err := run(10*time.Second, "", "pdbedit", "-L")
	if err != nil {
		return nil
	}
	var res []string
	for _, l := range strings.Split(out, "\n") {
		u := strings.SplitN(l, ":", 2)[0]
		if u != "" && u != "guest" {
			res = append(res, u)
		}
	}
	sort.Strings(res)
	return res
}

func validPassword(pw string) error {
	if len(pw) < 8 {
		return fmt.Errorf("Passwort: mindestens 8 Zeichen")
	}
	if strings.ContainsAny(pw, "\n\r:") {
		return fmt.Errorf("Passwort darf keine Zeilenumbrüche oder Doppelpunkte enthalten")
	}
	return nil
}

// setPassword setzt das Passwort für Anmeldung (shadow), Samba und – falls vorhanden – die TOS-Datenbank.
func (app *App) setPassword(user, pw string) error {
	h, err := sha512Hash(pw)
	if err != nil {
		return err
	}
	if err := setShadowPassword(user, h); err != nil {
		return err
	}
	if _, err := run(15*time.Second, pw+"\n"+pw+"\n", "smbpasswd", "-s", "-a", user); err != nil {
		return err
	}
	if app.cfg.TosNasDB != "" {
		// Hash besteht nur aus [A-Za-z0-9./$], Benutzername ist Base64 -> kein SQL-Sonderzeichen möglich
		b64 := base64.StdEncoding.EncodeToString([]byte(user))
		_, _ = run(10*time.Second, "", "sqlite3", app.cfg.TosNasDB, "update user_table set password='"+h+"' where username='"+b64+"'")
	}
	return nil
}

func (app *App) apiUsers(w http.ResponseWriter, r *http.Request, s *session) {
	admins := app.auth.admins()
	type u struct {
		Name  string `json:"name"`
		Admin bool   `json:"admin"`
		Me    bool   `json:"me"`
	}
	var res []u
	for _, n := range smbUsers() {
		res = append(res, u{n, contains(admins, n), n == s.user})
	}
	writeJSON(w, 200, map[string]any{"users": res})
}

func (app *App) apiUserPassword(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ User, Password string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	if !contains(smbUsers(), req.User) {
		writeErr(w, 404, "Benutzer unbekannt")
		return
	}
	if err := validPassword(req.Password); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	if err := app.setPassword(req.User, req.Password); err != nil {
		writeErr(w, 500, "Passwort konnte nicht gesetzt werden: %v", err)
		return
	}
	app.logf("%s: Passwort geändert für %s", s.user, req.User)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (app *App) apiUserCreate(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		User, Password string
		Admin          bool
	}
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	if !userNameRe.MatchString(req.User) {
		writeErr(w, 400, "Benutzername: Kleinbuchstaben, Ziffern, _ und -, max. 32 Zeichen")
		return
	}
	if err := validPassword(req.Password); err != nil {
		writeErr(w, 400, "%v", err)
		return
	}
	if _, err := shadowHash(req.User); err == nil {
		writeErr(w, 400, "Benutzer „%s“ existiert bereits im System", req.User)
		return
	}
	if _, err := run(15*time.Second, "", "adduser", "-D", "-H", "-s", "/bin/false", "-G", "allusers", req.User); err != nil {
		writeErr(w, 500, "Benutzer konnte nicht angelegt werden: %v", err)
		return
	}
	if err := app.setPassword(req.User, req.Password); err != nil {
		writeErr(w, 500, "Passwort konnte nicht gesetzt werden: %v", err)
		return
	}
	if req.Admin {
		_ = app.auth.saveAdmins(append(app.auth.admins(), req.User))
	}
	app.logf("%s: Benutzer angelegt: %s (Admin: %v)", s.user, req.User, req.Admin)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (app *App) apiUserDelete(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct{ User string }
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	if req.User == s.user {
		writeErr(w, 400, "Du kannst dich nicht selbst löschen")
		return
	}
	if !contains(smbUsers(), req.User) {
		writeErr(w, 404, "Benutzer unbekannt")
		return
	}
	_, _ = run(15*time.Second, "", "smbpasswd", "-x", req.User)
	_, _ = run(15*time.Second, "", "deluser", req.User)
	var keep []string
	for _, a := range app.auth.admins() {
		if a != req.User {
			keep = append(keep, a)
		}
	}
	_ = app.auth.saveAdmins(keep)
	// aus allen Freigaben entfernen
	app.shares.mu.Lock()
	if list, err := app.shares.load(); err == nil {
		for i := range list {
			delete(list[i].Users, req.User)
		}
		if app.applySmb(list) == nil {
			_ = app.shares.save(list)
		}
	}
	app.shares.mu.Unlock()
	app.logf("%s: Benutzer gelöscht: %s", s.user, req.User)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (app *App) apiUserAdmin(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		User  string
		Admin bool
	}
	if err := readBody(r, &req); err != nil {
		writeErr(w, 400, "Ungültige Anfrage")
		return
	}
	if !contains(smbUsers(), req.User) {
		writeErr(w, 404, "Benutzer unbekannt")
		return
	}
	if !req.Admin && req.User == s.user {
		writeErr(w, 400, "Du kannst dir die Admin-Rechte nicht selbst entziehen")
		return
	}
	var list []string
	for _, a := range app.auth.admins() {
		if a != req.User {
			list = append(list, a)
		}
	}
	if req.Admin {
		list = append(list, req.User)
	}
	if err := app.auth.saveAdmins(list); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	app.logf("%s: Admin-Recht %s: %v", s.user, req.User, req.Admin)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
