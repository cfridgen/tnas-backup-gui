package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRcloneConfRoundtrip(t *testing.T) {
	dir := t.TempDir()
	app := &App{cfg: &Config{StateDir: dir}}
	if err := app.confSet("od", [][2]string{{"type", "onedrive"}, {"token", `{"a":"b = c"}`}}); err != nil {
		t.Fatal(err)
	}
	if err := app.confSet("gd", [][2]string{{"type", "drive"}, {"scope", "drive"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.confSet("od_crypt", [][2]string{{"type", "crypt"}, {"remote", "od:TNAS-verschluesselt"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.confDelete("od", "od_crypt"); err != nil {
		t.Fatal(err)
	}
	secs, order := app.confSections()
	if len(order) != 1 || order[0] != "gd" || secs["gd"][1][1] != "drive" {
		t.Fatalf("unerwartet: %v %v", order, secs)
	}
	fi, _ := os.Stat(filepath.Join(dir, "rclone.conf"))
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("Rechte %v", fi.Mode().Perm())
	}
	// Token mit " = " im Wert bleibt erhalten
	_ = app.confSet("x", [][2]string{{"token", `{"a":"b = c"}`}})
	secs, _ = app.confSections()
	if secs["x"][0][1] != `{"a":"b = c"}` {
		t.Fatalf("Token verändert: %q", secs["x"][0][1])
	}
}

func TestCloudNames(t *testing.T) {
	for n, ok := range map[string]bool{"onedrive": true, "gdrive": true, "a": false, "Onedrive": false, "x y": false, "od_crypt": true} {
		if cloudNameRe.MatchString(n) != ok {
			t.Errorf("%q: erwartet %v", n, ok)
		}
	}
}
