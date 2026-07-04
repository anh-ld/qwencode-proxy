package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The cap is the whole point: a long debug session must not fill the disk.
func TestDumperCap(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "dump-*.log")
	if err != nil {
		t.Fatal(err)
	}
	d := &dumper{f: f, max: 10}

	// Writes report full length (so a TeeReader never short-circuits) but the file stops at max.
	if n, _ := d.Write([]byte("12345")); n != 5 {
		t.Errorf("Write reported %d, want 5", n)
	}
	if n, _ := d.Write([]byte("ABCDEFGHIJ")); n != 10 { // only 5 bytes of room left
		t.Errorf("Write reported %d, want 10", n)
	}
	d.section("IGNORED", []byte("more")) // past the cap: dropped entirely
	_ = f.Close()

	got, err := os.ReadFile(filepath.Join(filepath.Dir(f.Name()), filepath.Base(f.Name())))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Errorf("file grew past cap: %d bytes = %q", len(got), got)
	}
}

// A nil dumper is a valid no-op receiver — every call site relies on this.
func TestNilDumperNoPanic(t *testing.T) {
	var d *dumper
	d.section("x", []byte("y"))
	d.header("z")
	if n, _ := d.Write([]byte("abc")); n != 3 {
		t.Errorf("nil Write reported %d, want 3", n)
	}
	d.close()
}
