package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWALTruncateAboveKey(t *testing.T) {
	e := env(map[string]string{"HOME": "/h"})
	if got := Defaults(e).WALTruncateAbove; got != 512<<20 {
		t.Fatalf("default wal.truncate_above = %d, want 512 MiB", got)
	}
	load := func(body string) (Config, error) {
		p := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(p, true, e)
	}
	c, err := load("[wal]\ntruncate_above = 1048576\n")
	if err != nil || c.WALTruncateAbove != 1<<20 {
		t.Fatalf("%v %d", err, c.WALTruncateAbove)
	}
	// 0 disables TRUNCATE and must survive as 0, not fall back to the default.
	c, err = load("[wal]\ntruncate_above = 0\n")
	if err != nil || c.WALTruncateAbove != 0 {
		t.Fatalf("%v %d", err, c.WALTruncateAbove)
	}
	if _, err := load("[wal]\ntruncate_above = -1\n"); err == nil {
		t.Fatal("a negative wal.truncate_above was accepted")
	}
}
