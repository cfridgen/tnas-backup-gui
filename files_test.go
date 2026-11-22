package main

import "testing"

func TestFileKind(t *testing.T) {
	cases := map[string][2]string{
		"film.avi":    {"video", "video/x-msvideo"},
		"FILM.MPG":    {"video", "video/mpeg"},
		"aufnahme.ts": {"video", "video/mp2t"},
		"film.mp4":    {"video", "video/mp4"},
		"lied.mp3":    {"audio", "audio/mpeg"},
		"bild.jpg":    {"image", "image/jpeg"},
		"doku.pdf":    {"pdf", "application/pdf"},
		"grafik.svg":  {"text", "text/plain"},
		"notizen.txt": {"text", "text/plain"},
	}
	for name, want := range cases {
		k, m := fileKind(name, []byte("hallo welt"))
		if k != want[0] || m != want[1] {
			t.Errorf("%s: %s %s, erwartet %s %s", name, k, m, want[0], want[1])
		}
	}
	if k, _ := fileKind("daten.bin", []byte{0, 1, 2, 3, 0, 5}); k != "binary" {
		t.Errorf("Binärdatei als %s erkannt", k)
	}
}
