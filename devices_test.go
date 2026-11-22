package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// testAuth baut eine Umgebung mit eigener shadow-Datei und Admin-Liste.
func testAuth(t *testing.T) *authState {
	t.Helper()
	dir := t.TempDir()
	h, err := sha512Hash("geheim123")
	if err != nil {
		t.Fatal(err)
	}
	shadowPath = filepath.Join(dir, "shadow")
	if err := os.WriteFile(shadowPath, []byte("alice:"+h+":20000:0:99999:7:::\nbob:"+h+":20000:0:99999:7:::\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.StateDir = dir
	cfg.TosNasDB = ""
	a := newAuth(&cfg)
	a.logf = t.Logf
	if err := a.saveAdmins([]string{"alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	return a
}

func req(ip string, cookies []*http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.RemoteAddr = ip + ":50000"
	r.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/157")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

func deviceCookieFrom(w *httptest.ResponseRecorder) []*http.Cookie {
	var res []*http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == deviceCookie && c.Value != "" {
			res = append(res, c)
		}
	}
	return res
}

func TestPasswordCheck(t *testing.T) {
	testAuth(t)
	if !checkPassword("alice", "geheim123") {
		t.Fatal("richtiges Passwort abgelehnt")
	}
	if checkPassword("alice", "falsch") || checkPassword("niemand", "geheim123") {
		t.Fatal("falsches Passwort/Benutzer angenommen")
	}
}

func TestAutoLogin(t *testing.T) {
	a := testAuth(t)
	w := httptest.NewRecorder()
	a.rememberDevice(w, req("192.168.1.22", nil), "alice")
	ck := deviceCookieFrom(w)
	if len(ck) != 1 || ck[0].MaxAge < 300*86400 || !ck[0].HttpOnly {
		t.Fatalf("Geräte-Cookie fehlt/falsch: %+v", ck)
	}
	// gleiche IP -> angemeldet
	if s := a.autoLogin(httptest.NewRecorder(), req("192.168.1.22", ck)); s == nil || s.user != "alice" {
		t.Fatal("automatische Anmeldung von gleicher IP fehlgeschlagen")
	}
	// andere IP -> nein
	if a.autoLogin(httptest.NewRecorder(), req("192.168.1.99", ck)) != nil {
		t.Fatal("automatische Anmeldung von fremder IP angenommen")
	}
	// gefälschtes Token -> nein
	fake := []*http.Cookie{{Name: deviceCookie, Value: randHex(32)}}
	if a.autoLogin(httptest.NewRecorder(), req("192.168.1.22", fake)) != nil {
		t.Fatal("gefälschtes Token angenommen")
	}
	// Datei enthält nur den Hash, nicht das Token
	b, _ := os.ReadFile(a.deviceFile())
	if len(b) == 0 || containsStr(string(b), ck[0].Value) {
		t.Fatal("Token im Klartext gespeichert")
	}
	// Admin-Recht entzogen -> nein
	_ = a.saveAdmins([]string{"bob"})
	if a.autoLogin(httptest.NewRecorder(), req("192.168.1.22", ck)) != nil {
		t.Fatal("Nicht-Admin automatisch angemeldet")
	}
	_ = a.saveAdmins([]string{"alice", "bob"})
	// Konto gesperrt -> nein
	h, _ := sha512Hash("geheim123")
	_ = os.WriteFile(shadowPath, []byte("alice:!"+h+":20000:::::\n"), 0600)
	if a.autoLogin(httptest.NewRecorder(), req("192.168.1.22", ck)) != nil {
		t.Fatal("gesperrtes Konto automatisch angemeldet")
	}
}

func TestForgetAndRevoke(t *testing.T) {
	a := testAuth(t)
	w1 := httptest.NewRecorder()
	a.rememberDevice(w1, req("192.168.1.22", nil), "alice")
	mac := deviceCookieFrom(w1)
	w2 := httptest.NewRecorder()
	a.rememberDevice(w2, req("192.168.1.77", nil), "alice")
	vader := deviceCookieFrom(w2)
	w3 := httptest.NewRecorder()
	a.rememberDevice(w3, req("192.168.1.77", nil), "bob")
	bob := deviceCookieFrom(w3)

	// Passwortwechsel am Mac: vader-Gerät von alice weg, Mac bleibt, bob unberührt
	a.revokeUserDevices("alice", req("192.168.1.22", mac))
	if a.autoLogin(httptest.NewRecorder(), req("192.168.1.22", mac)) == nil {
		t.Fatal("aktuelles Gerät nach Passwortwechsel entfernt")
	}
	if a.autoLogin(httptest.NewRecorder(), req("192.168.1.77", vader)) != nil {
		t.Fatal("anderes Gerät nach Passwortwechsel noch gültig")
	}
	if a.autoLogin(httptest.NewRecorder(), req("192.168.1.77", bob)) == nil {
		t.Fatal("Gerät eines anderen Benutzers entfernt")
	}
	// Abmelden vergisst das Gerät
	a.forgetDevice(httptest.NewRecorder(), req("192.168.1.22", mac))
	if a.autoLogin(httptest.NewRecorder(), req("192.168.1.22", mac)) != nil {
		t.Fatal("Gerät nach Abmelden noch gültig")
	}
}

func containsStr(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
