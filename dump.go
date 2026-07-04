package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const defaultDumpMaxBytes = 10 << 20 // 10 MiB

func dumpPath() string { return filepath.Join(configDir(), "dump.log") }

// dumper appends chat traffic to one capped file; no rotation, stops at the cap — delete to reset.
type dumper struct {
	mu  sync.Mutex
	f   *os.File
	max int64
	n   int64
}

// newDumper opens the dump file when enabled; returns nil (a valid no-op receiver) when off or unopenable.
func newDumper(cfg Config) *dumper {
	if !cfg.Dump && os.Getenv("QP_DUMP") == "" {
		return nil
	}
	if os.MkdirAll(configDir(), 0o700) != nil {
		return nil
	}
	f, err := os.OpenFile(dumpPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	_ = f.Chmod(0o600) // O_CREATE perm is umask-masked and skipped for an existing file; enforce it
	max := int64(cfg.DumpMaxBytes)
	if max <= 0 {
		max = defaultDumpMaxBytes
	}
	d := &dumper{f: f, max: max}
	if info, err := f.Stat(); err == nil {
		d.n = info.Size() // pre-existing bytes count toward the cap
	}
	return d
}

// section writes a labeled header then the payload; both count against the cap.
func (d *dumper) section(tag string, b []byte) {
	if d == nil {
		return
	}
	d.header(tag)
	_, _ = d.Write(b)
}

func (d *dumper) header(tag string) {
	_, _ = d.Write([]byte(fmt.Sprintf("\n===== %s %s =====\n", tag, time.Now().Format(time.RFC3339))))
}

// Write: capped serialized append (io.Writer, for teeing); reports full write always — best-effort, never errors a tee.
func (d *dumper) Write(b []byte) (int, error) {
	if d == nil {
		return len(b), nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if room := d.max - d.n; room > 0 {
		w := b
		if int64(len(w)) > room {
			w = w[:room]
		}
		nn, _ := d.f.Write(w)
		d.n += int64(nn)
	}
	return len(b), nil
}

func (d *dumper) close() {
	if d == nil {
		return
	}
	_ = d.f.Close()
}
