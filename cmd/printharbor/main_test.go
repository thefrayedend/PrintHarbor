package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// prog makes file content big enough to pass for a program.
func prog(tag string) []byte { return append([]byte(tag), make([]byte, 2048)...) }

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "(missing)"
	}
	return string(bytes.TrimRight(b, "\x00"))
}

// swapIn is the heart of the update: the waiting file takes the program's
// place, the old one is kept as .old, and it can all be put back.
func TestSwapIn(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "printharbor-windows-amd64.exe")
	next := filepath.Join(dir, "printharbor-windows-amd64.new.exe")
	os.WriteFile(exe, prog("v1"), 0o755)

	// Nothing waiting: nothing happens.
	if ok, _, err := swapIn(exe); ok || err != nil || read(t, exe) != "v1" {
		t.Fatalf("no update: ok=%v err=%v exe=%s", ok, err, read(t, exe))
	}
	// An update waiting, and a stale .old from last time.
	os.WriteFile(next, prog("v2"), 0o755)
	os.WriteFile(exe+".old", prog("v0"), 0o755)
	ok, undo, err := swapIn(exe)
	if !ok || err != nil {
		t.Fatalf("swap: ok=%v err=%v", ok, err)
	}
	if read(t, exe) != "v2" || read(t, exe+".old") != "v1" || read(t, next) != "(missing)" {
		t.Errorf("after swap: exe=%s old=%s new=%s", read(t, exe), read(t, exe+".old"), read(t, next))
	}
	// Putting it back (the new copy wouldn't start): the update waits again.
	undo()
	if read(t, exe) != "v1" || read(t, next) != "v2" {
		t.Errorf("after undo: exe=%s new=%s", read(t, exe), read(t, next))
	}
	// An update for a program that isn't there yet (first app-window install).
	app := filepath.Join(dir, "printharbor-app.exe")
	os.WriteFile(filepath.Join(dir, "printharbor-app.new.exe"), prog("app1"), 0o755)
	if ok, _, err := swapIn(app); !ok || err != nil || read(t, app) != "app1" {
		t.Errorf("first install: ok=%v err=%v app=%s", ok, err, read(t, app))
	}
	// The waiting "update" isn't a program at all (a folder, an empty file from
	// an interrupted copy). The program that was there must still be there.
	os.Remove(next)
	os.WriteFile(exe, prog("v1"), 0o755)
	blocker := filepath.Join(dir, "blocked.exe")
	os.WriteFile(blocker, prog("b1"), 0o755)
	os.MkdirAll(filepath.Join(dir, "blocked.new.exe", "x"), 0o755)
	if ok, _, err := swapIn(blocker); ok || err == nil {
		t.Errorf("a folder was installed as an update: ok=%v err=%v", ok, err)
	}
	os.RemoveAll(filepath.Join(dir, "blocked.new.exe"))
	os.WriteFile(filepath.Join(dir, "blocked.new.exe"), nil, 0o755)
	if ok, _, err := swapIn(blocker); ok || err == nil {
		t.Errorf("an empty file was installed as an update: ok=%v err=%v", ok, err)
	}
	if read(t, blocker) != "b1" {
		t.Errorf("a refused update lost the program: %s", read(t, blocker))
	}
}
