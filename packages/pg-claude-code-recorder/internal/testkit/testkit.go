// Package testkit builds a throwaway store with copied files, a parser and a
// query engine for the parser, query and search tests. It never touches a real
// Claude directory: copies are written straight into the store.
package testkit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// ClaudeDir is the fake Claude directory the copies pretend to come from.
const ClaudeDir = "/claude"

// Kit is the assembled fixture.
type Kit struct {
	T      *testing.T
	Dir    string
	Store  *store.Store
	Obs    *obs.Obs
	Parser *parser.Parser
	Engine *query.Engine
	clock  int64
	sizes  map[int64]int64
	gens   map[int64]int
}

// New returns a Kit over a fresh state directory.
func New(t *testing.T) *Kit {
	t.Helper()
	k := &Kit{T: t, Dir: t.TempDir(), sizes: map[int64]int64{}, gens: map[int64]int{}}
	k.open()
	t.Cleanup(func() { _ = k.Store.Close() })
	return k
}

func (k *Kit) open() {
	k.T.Helper()
	k.Obs = obs.New(nil)
	st, err := store.Open(k.Dir, store.Options{Obs: k.Obs})
	if err != nil {
		k.T.Fatal(err)
	}
	k.Store = st
	srcs := sources.All()
	k.Parser = parser.New(parser.Config{Store: st, Sources: srcs, ClaudeDir: ClaudeDir, Obs: k.Obs})
	k.Engine = &query.Engine{DB: st.Read(), Catalog: sources.Catalog(srcs), Res: k.Parser}
}

// Reopen closes and reopens the store (a restart).
func (k *Kit) Reopen() {
	k.T.Helper()
	_ = k.Store.Close()
	k.open()
}

func (k *Kit) now() int64 { k.clock += 1000; return 1_700_000_000_000_000_000 + k.clock }

// Add creates a file named name under project (history.jsonl is the history
// source) holding content as its first generation, copied whole.
func (k *Kit) Add(project, name, content string) int64 {
	k.T.Helper()
	id := k.Store.NewFileID()
	src, path := "claude-transcript", filepath.Join(ClaudeDir, "projects", project, name)
	if name == "history.jsonl" {
		src, path = "claude-history", filepath.Join(ClaudeDir, name)
	}
	at := k.now()
	if err := k.Store.Commit(
		&store.Rec{T: store.TFile, At: at, File: id, Source: src, Mode: "append", Dev: 1, Ino: uint64(id)},
		&store.Rec{T: store.TPath, At: at, File: id, Path: path},
		&store.Rec{T: store.TGen, At: at, File: id, Gen: 1, Method: "bytes", Reason: "first"},
	); err != nil {
		k.T.Fatal(err)
	}
	k.gens[id] = 1
	k.write(id, 1, 0, content)
	return id
}

// AddAgent creates a subagent transcript file of a session.
func (k *Kit) AddAgent(project, session, agent, content string) int64 {
	k.T.Helper()
	id := k.Store.NewFileID()
	path := filepath.Join(ClaudeDir, "projects", project, session, "subagents", "agent-"+agent+".jsonl")
	at := k.now()
	if err := k.Store.Commit(
		&store.Rec{T: store.TFile, At: at, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: uint64(id)},
		&store.Rec{T: store.TPath, At: at, File: id, Path: path},
		&store.Rec{T: store.TGen, At: at, File: id, Gen: 1, Method: "bytes", Reason: "first"},
	); err != nil {
		k.T.Fatal(err)
	}
	k.gens[id] = 1
	k.write(id, 1, 0, content)
	return id
}

// Append copies more bytes onto the file's current generation.
func (k *Kit) Append(id int64, more string) {
	k.T.Helper()
	k.write(id, k.gens[id], k.sizes[id], more)
}

// NewGeneration starts generation gen+1 holding content (a reset).
func (k *Kit) NewGeneration(id int64, content string) {
	k.T.Helper()
	if _, err := k.Store.StartGeneration(id, "test", "bytes", k.now()); err != nil {
		k.T.Fatal(err)
	}
	k.gens[id]++
	k.sizes[id] = 0
	k.write(id, k.gens[id], 0, content)
}

func (k *Kit) write(id int64, gen int, from int64, content string) {
	k.T.Helper()
	cp := k.Store.CopyPath(id, gen)
	fh, err := os.OpenFile(cp, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		k.T.Fatal(err)
	}
	if _, err := fh.WriteAt([]byte(content), from); err != nil {
		k.T.Fatal(err)
	}
	_ = fh.Close()
	to := from + int64(len(content))
	k.sizes[id] = to
	if err := k.Store.Commit(&store.Rec{T: store.TCommit, At: k.now(), File: id, Gen: gen, From: from, To: to, Size: to, Tail: 1, TailLen: 1}); err != nil {
		k.T.Fatal(err)
	}
}

// Parse parses every file.
func (k *Kit) Parse() {
	k.T.Helper()
	if err := k.Parser.ParseAll(); err != nil {
		k.T.Fatal(err)
	}
}

// Count returns the single integer SELECT q yields.
func (k *Kit) Count(q string, args ...any) int {
	k.T.Helper()
	var n int
	if err := k.Store.Read().QueryRow(q, args...).Scan(&n); err != nil {
		k.T.Fatal(err)
	}
	return n
}
