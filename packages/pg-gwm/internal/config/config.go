package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/phillipgreenii/pg-gwm/internal/repo"
)

// RepoConfig is one repository's configuration; the TOML keys are the
// behavior README's names.
type RepoConfig struct {
	WorktreeRoot        string   `toml:"worktree-root"`
	WorktreeDirTemplate string   `toml:"worktree-dir-template"`
	Min                 *int     `toml:"min"`
	Max                 *int     `toml:"max"`
	Reuse               bool     `toml:"reuse-reclaimed-worktrees"`
	Clean               bool     `toml:"clean-reused-worktree"`
	KeepList            []string `toml:"keep-list"`
	ReuseHookPolicy     string   `toml:"reuse-hook-policy"`
	FreeSpaceFloor      *uint64  `toml:"free-space-floor"`
	DefaultTTL          Duration `toml:"default-ttl"`
	LockWait            Duration `toml:"lock-wait"`
	GitTimeout          Duration `toml:"git-timeout"`
	CreateTimeout       Duration `toml:"create-timeout"`
	IndexLockStaleAfter Duration `toml:"index-lock-stale-after"`
	TombstoneRetention  Duration `toml:"tombstone-retention"`
	// Exceptions are the standing exceptions: worktrees that legitimately live
	// outside pg-gwm. Declared as [[repos."<key>".exceptions]] tables.
	Exceptions []Exception `toml:"exceptions"`
}

// File is the whole configuration file: one table per repository, keyed by the
// main checkout's real path.
type File struct {
	Repos map[string]RepoConfig `toml:"repos"`
}

// DefaultPath is $XDG_CONFIG_HOME/pg-gwm/config.toml, else
// ~/.config/pg-gwm/config.toml.
func DefaultPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "pg-gwm", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pg-gwm", "config.toml")
}

// Load reads the file at path. A missing file is an empty File; an unknown key
// is an error.
func Load(path string) (File, error) {
	var f File
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("config %s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		keys := make([]string, len(u))
		for i, k := range u {
			keys[i] = k.String()
		}
		return File{}, fmt.Errorf("config %s: unknown key %s", path, strings.Join(keys, ", "))
	}
	return f, nil
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// withDefaults fills every unset field with the README default.
func withDefaults(c RepoConfig, main string) RepoConfig {
	if c.WorktreeRoot == "" {
		c.WorktreeRoot = main + ".worktrees"
	}
	if c.WorktreeDirTemplate == "" {
		c.WorktreeDirTemplate = "{{repo}}-wt-{{index}}"
	}
	if c.ReuseHookPolicy == "" {
		c.ReuseHookPolicy = "run"
	}
	if c.DefaultTTL.IsZero() {
		c.DefaultTTL = NoDuration
	}
	for _, d := range []struct {
		p *Duration
		v string
	}{
		{&c.LockWait, "10s"},
		{&c.GitTimeout, "2m"},
		{&c.CreateTimeout, "30m"},
		{&c.IndexLockStaleAfter, "10m"},
		{&c.TombstoneRetention, "7d"},
	} {
		if d.p.IsZero() {
			*d.p, _ = ParseDuration(d.v)
		}
	}
	return c
}

// For returns the configuration of r: the table whose key names the same real
// path as r.Main, with unset fields at the README defaults.
func (f File) For(r repo.Repo) RepoConfig {
	want := realPath(r.Main)
	var c RepoConfig
	for k, v := range f.Repos {
		if realPath(k) == want {
			c = v
			break
		}
	}
	return withDefaults(c, r.Main)
}
