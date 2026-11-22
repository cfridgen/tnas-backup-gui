package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStream(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "film.avi")
	if err := os.WriteFile(p, []byte("0123456789abcdef"), 0644); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	streams.Lock()
	streams.m["gut"] = streamTok{path: p, ip: "192.168.1.22", expires: time.Now().Add(time.Hour)}
	streams.m["alt"] = streamTok{path: p, ip: "192.168.1.22", expires: time.Now().Add(-time.Minute)}
	streams.Unlock()

	get := func(tok, ip, rng string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/stream/"+tok+"/film.avi", nil)
		r.RemoteAddr = ip + ":40000"
		if rng != "" {
			r.Header.Set("Range", rng)
		}
		w := httptest.NewRecorder()
		app.handleStream(w, r)
		return w
	}
	if w := get("gut", "192.168.1.22", ""); w.Code != 200 || w.Body.String() != "0123456789abcdef" || w.Header().Get("Content-Type") != "video/x-msvideo" {
		t.Fatalf("Stream: %d %q %s", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if w := get("gut", "192.168.1.22", "bytes=4-7"); w.Code != 206 || w.Body.String() != "4567" {
		t.Fatalf("Range: %d %q", w.Code, w.Body.String())
	}
	if w := get("gut", "192.168.1.99", ""); w.Code != 403 {
		t.Fatalf("fremde IP: %d", w.Code)
	}
	if w := get("alt", "192.168.1.22", ""); w.Code != 403 {
		t.Fatalf("abgelaufen: %d", w.Code)
	}
	if w := get("gibtsnicht", "192.168.1.22", ""); w.Code != 403 {
		t.Fatalf("unbekannt: %d", w.Code)
	}
}
