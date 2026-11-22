package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// run führt ein Programm ohne Shell aus (keine Shell-Injection möglich) und liefert stdout.
func run(timeout time.Duration, stdin string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return out.String(), fmt.Errorf("%s: %v %s", name, err, msg)
	}
	return out.String(), nil
}

// writeFileAtomic schreibt über eine temporäre Datei + rename, damit nie halbe Dateien entstehen.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, format string, a ...any) {
	writeJSON(w, status, map[string]string{"error": fmt.Sprintf(format, a...)})
}

func readBody(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// pidOf liefert true, wenn ein Prozess mit diesem Namen läuft (über /proc, ohne externe Tools).
func pidOf(name string) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		if readTrim("/proc/"+e.Name()+"/comm") == name {
			return true
		}
	}
	return false
}

// insideDir prüft, ob p (bereinigt) innerhalb von base liegt.
func insideDir(base, p string) bool {
	base = filepath.Clean(base)
	p = filepath.Clean(p)
	return p != base && strings.HasPrefix(p, base+"/")
}
