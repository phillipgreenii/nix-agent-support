// Package pn is a read-only client for `pn workspace info --json`. The schema is
// the stable consumed API pinned by phillipg-nix-repo-base ADR 0012:
// {wsid, root, terminal, repos:[{name, path, applied_ref, dirty}]}.
// applied_ref is always present and is "" (never null) when a repo has no
// applied-state record. pb never reads pn's files directly.
//
// VERIFIED: real pn emits a BARE object (repo-base modules/pn/internal/cli emits
// enc.Encode(info) with no {data} envelope). The client tolerates an envelope
// too, defensively, since pn's --json behaviour may evolve.
package pn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/pb/internal/run"
)

type Repo struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// AppliedRef is the repo's local HEAD at apply time — evidence that an apply
	// RAN over that checkout. It is NOT evidence that the applied system CONTAINS
	// the commit: for a repo the terminal pins as a `github:` flake input the code
	// reaches the build only through the terminal's flake.lock, so LockedRev below
	// is the second, independent fact a gate needs (repo-base ADR 0025).
	AppliedRef string `json:"applied_ref"`
	Dirty      bool   `json:"dirty"`
	// AppliedStateSchema is pn's applied-state schema version for this repo's
	// record. 0 means the record predates locked_revs — so TerminalInput and
	// LockedRev carry NO information and MUST NOT be read as negative evidence.
	AppliedStateSchema int `json:"applied_state_schema"`
	// TerminalInput reports whether the apply consumed this repo as a flake input
	// of the terminal. Meaningful only when AppliedStateSchema >= 2.
	TerminalInput bool `json:"terminal_input"`
	// LockedRev is the rev the TERMINAL's flake.lock pinned for this repo at that
	// apply — recorded WITH the apply, so it is not disturbed by a later relock.
	// Empty while TerminalInput is true means the apply could not establish it.
	LockedRev string `json:"locked_rev"`
}

type Info struct {
	Wsid     string `json:"wsid"`
	Root     string `json:"root"`
	Terminal string `json:"terminal"`
	Repos    []Repo `json:"repos"`
}

func (i Info) RepoByName(name string) (Repo, bool) {
	for _, r := range i.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return Repo{}, false
}

type Client struct {
	R run.Runner
}

// envelope is the optional bd/pn JSON envelope wrapper.
type envelope struct {
	Data json.RawMessage `json:"data"`
}

// Info runs `pn workspace info --json` with cwd=dir and unmarshals the result.
func (c Client) Info(ctx context.Context, dir string) (Info, error) {
	res, err := c.R.Run(ctx, "pn", []string{"workspace", "info", "--json"}, run.Options{Dir: dir})
	if err != nil {
		return Info{}, fmt.Errorf("pn workspace info (is %q a pn workspace?): %w", dir, err)
	}
	var info Info
	// Tolerate both the enveloped form ({"data":{…}}) and a bare object.
	var env envelope
	if e := json.Unmarshal([]byte(res.Stdout), &env); e == nil && len(env.Data) > 0 {
		if e2 := json.Unmarshal(env.Data, &info); e2 != nil {
			return Info{}, fmt.Errorf("parse pn info data: %w", e2)
		}
	} else if e := json.Unmarshal([]byte(res.Stdout), &info); e != nil {
		return Info{}, fmt.Errorf("parse pn info: %w", e)
	}
	if info.Root == "" {
		return Info{}, errors.New("pn workspace info returned empty root")
	}
	return info, nil
}
