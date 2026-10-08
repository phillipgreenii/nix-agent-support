package store_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
)

func dirListing(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// INV-LOG-30: the offline check reports lines, batches, the recoveries it
// would perform and the line of any corruption, without modifying the log.
func TestCheckOffline(t *testing.T) {
	committed, all := committedLog(t)
	garbage := fixtureLine(t, "garbage-line.jsonl")
	v2 := fixtureLine(t, "unknown-version-line.jsonl")
	members := lines(t, memberEvent(5, batchID(2)), memberEvent(6, batchID(2)))

	cases := []struct {
		name     string
		log      []byte
		lines    int
		batches  int
		recovery store.Recovery
		problem  string // "", "corrupt" or "version"
		line     int
	}{
		{name: "a clean log", log: committed, lines: 4, batches: 1},
		{name: "an empty log", log: nil},
		{
			name: "a torn tail", log: join(committed, garbage), lines: 4, batches: 1,
			recovery: store.Recovery{TornTail: true, TruncatedBytes: int64(len(garbage))},
		},
		{
			name: "an uncommitted batch", log: join(committed, members), lines: 4, batches: 1,
			recovery: store.Recovery{UncommittedBatches: 1, TruncatedBytes: int64(len(members))},
		},
		{
			name: "an uncommitted batch and a torn line", log: join(committed, members, garbage), lines: 4, batches: 1,
			recovery: store.Recovery{TornTail: true, UncommittedBatches: 1, TruncatedBytes: int64(len(members) + len(garbage))},
		},
		{name: "corruption mid-file", log: join(committed, garbage, lines(t, plainEvent(9))), problem: "corrupt", line: 5},
		{name: "an unknown version", log: join(committed, v2), problem: "version", line: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := seedLog(t, tc.log)
			before := dirListing(t, dir)

			rep, err := store.Check(dir)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if rep.Lines != tc.lines || rep.Batches != tc.batches {
				t.Errorf("Lines, Batches = %d, %d, want %d, %d", rep.Lines, rep.Batches, tc.lines, tc.batches)
			}
			if rep.Recovery != tc.recovery {
				t.Errorf("Recovery = %+v, want %+v", rep.Recovery, tc.recovery)
			}
			if rep.Size != int64(len(tc.log)) {
				t.Errorf("Size = %d, want %d", rep.Size, len(tc.log))
			}
			var corrupt *store.CorruptError
			var version *store.UnknownVersionError
			switch tc.problem {
			case "":
				if rep.Problem != nil {
					t.Errorf("Problem = %v, want none", rep.Problem)
				}
				if tc.lines == len(all) && !slices.Equal(ids(rep.Events), ids(all)) {
					t.Errorf("Events = %v, want %v", ids(rep.Events), ids(all))
				}
			case "corrupt":
				if !errors.As(rep.Problem, &corrupt) || corrupt.Line != tc.line {
					t.Errorf("Problem = %v, want a *CorruptError at line %d", rep.Problem, tc.line)
				}
			case "version":
				if !errors.As(rep.Problem, &version) || version.Line != tc.line {
					t.Errorf("Problem = %v, want an *UnknownVersionError at line %d", rep.Problem, tc.line)
				}
			}

			// It never modifies anything: not the log, not the directory.
			if got := readLog(t, dir); !bytes.Equal(got, tc.log) {
				t.Errorf("Check modified the log")
			}
			if after := dirListing(t, dir); !slices.Equal(after, before) {
				t.Errorf("directory entries changed from %v to %v", before, after)
			}
		})
	}

	t.Run("it works on a directory a service has open, and takes no lock", func(t *testing.T) {
		dir := seedLog(t, join(committed, garbage))
		// The torn tail makes the holder's own recovery rewrite the log, so
		// seed the holder from a clean copy and add the tail afterwards.
		if err := os.WriteFile(logPath(dir), committed, 0o600); err != nil {
			t.Fatal(err)
		}
		holder, _, _ := openStore(t, dir, nil)
		_ = holder
		if err := os.WriteFile(logPath(dir), join(committed, garbage), 0o600); err != nil {
			t.Fatal(err)
		}

		rep, err := store.Check(dir)
		if err != nil {
			t.Fatalf("Check while the store is open: %v", err)
		}
		if !rep.Recovery.TornTail || rep.Lines != 4 {
			t.Errorf("report = %+v, want a torn tail after 4 lines", rep)
		}
		if got := readLog(t, dir); !bytes.Equal(got, join(committed, garbage)) {
			t.Errorf("Check modified the log of an open store")
		}
	})

	t.Run("it accepts the log file itself", func(t *testing.T) {
		dir := seedLog(t, committed)
		rep, err := store.Check(logPath(dir))
		if err != nil || rep.Lines != 4 {
			t.Fatalf("Check(file) = %+v, %v, want 4 lines", rep, err)
		}
	})

	t.Run("a missing log is an error and is not created", func(t *testing.T) {
		dir := t.TempDir()
		_, err := store.Check(dir)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Check error = %v, want it to wrap fs.ErrNotExist", err)
		}
		if got := dirListing(t, dir); len(got) != 0 {
			t.Errorf("Check created %v", got)
		}
	})
}

// INTF-LOG: the data directory is the user's data home, in a pg-task-focus
// directory; XDG_DATA_HOME decides where when it is set.
func TestDefaultDir(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"XDG_DATA_HOME wins", map[string]string{"XDG_DATA_HOME": "/xdg/data", "HOME": "/home/op"}, "/xdg/data/pg-task-focus"},
		{"otherwise HOME/.local/share", map[string]string{"HOME": "/home/op"}, "/home/op/.local/share/pg-task-focus"},
		{"an empty XDG_DATA_HOME is unset", map[string]string{"XDG_DATA_HOME": "", "HOME": "/home/op"}, "/home/op/.local/share/pg-task-focus"},
		{"a relative XDG_DATA_HOME is ignored", map[string]string{"XDG_DATA_HOME": "relative/data", "HOME": "/home/op"}, "/home/op/.local/share/pg-task-focus"},
		{"nothing to build it from is empty, never a relative path", map[string]string{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.DefaultDir(env(tc.env)); got != tc.want {
				t.Errorf("DefaultDir = %q, want %q", got, tc.want)
			}
		})
	}
}
