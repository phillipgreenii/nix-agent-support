// Package invariants is the shared helper every test runs after it finishes.
// It checks the properties the recorder promises at all times:
//
//   - the parse offset is at most the copy offset;
//   - the copy's length equals its committed offset after a restart, and its
//     bytes equal the original's prefix;
//   - no orphan temporary files remain;
//   - the journal replay equals the database's observations;
//   - the Claude directory is unchanged unless removal (or extract, which
//     writes into it) is enabled.
//
// Checks that need later packets (every record pointer resolves, the feed
// sequence is gapless and monotonic, a removed Conversation has a verified
// archive and a manifest) are added by those packets through the ArchiveCompare
// hook and further checks; they are no-ops here because the tables do not exist.
package invariants

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// quarantineDir is the name of the directory removal quarantines files in
// (scope.QuarantineDir; repeated here so the helper imports nothing but the store).
const quarantineDir = ".pg-recorder-quarantine"

// RecordedBytes is the ArchiveCompare hook that compares a generation's copy file
// with the same bytes read through the record pointer (the archive when the
// settled part is there, else the copy): they MUST be equal wherever both exist.
func RecordedBytes(st *store.Store) func(file int64, gen int, copyPath string) error {
	return func(file int64, gen int, copyPath string) error {
		arch := st.ArchivedTo(file, gen)
		if arch == 0 {
			return nil
		}
		cp, err := os.ReadFile(copyPath)
		if err != nil || len(cp) == 0 {
			return nil
		}
		n := int64(len(cp))
		if n > arch {
			n = arch
		}
		if bytes.Count(cp[:n], []byte{0}) == int(n) {
			return nil // the archived part of a settled copy is a hole, not bytes
		}
		want, err := st.ReadPointer(file, gen, 0, int(n))
		if err != nil {
			return err
		}
		if !bytes.Equal(want, cp[:n]) {
			return fmt.Errorf("copy bytes differ from the archive over [0,%d)", n)
		}
		return nil
	}
}

// Options tunes Check.
type Options struct {
	// ClaudeBefore is a snapshot of the Claude directory taken before the test
	// acted. When set, the directory MUST be unchanged unless AllowClaudeChange.
	ClaudeBefore *DirSnapshot
	ClaudeDir    string
	// AllowClaudeChange is the extract exemption: extract (and enabled removal)
	// legitimately change the Claude directory.
	AllowClaudeChange bool
	// AllowQuarantine permits entries to remain in the quarantine directories
	// (a restore conflict keeps one on purpose). Otherwise any file in a
	// quarantine directory under the Claude directory is a stray.
	AllowQuarantine bool
	// ArchiveCompare compares a generation's copy with the archive that holds
	// it, or with the generation's recorded bytes; the settle and removal
	// packets provide it. A nil hook skips the comparison.
	ArchiveCompare func(file int64, gen int, copyPath string) error
}

// Check fails t for every violation.
func Check(t testing.TB, st *store.Store, o Options) {
	t.Helper()
	for _, v := range Violations(st, o) {
		t.Errorf("invariant violated: %s", v)
	}
}

// Violations returns every violated invariant.
func Violations(st *store.Store, o Options) []string {
	var out []string
	add := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }

	files, err := st.AllFiles()
	if err != nil {
		return []string{"listing files: " + err.Error()}
	}
	for _, f := range files {
		if f.Mode != "append" {
			continue
		}
		gens, err := st.Generations(f.ID)
		if err != nil {
			add("generations of %d: %v", f.ID, err)
			continue
		}
		for _, g := range gens {
			if g.ParseOffset > g.CopyOffset {
				add("file %d gen %d: parse offset %d exceeds copy offset %d", f.ID, g.Gen, g.ParseOffset, g.CopyOffset)
			}
			cp := st.CopyPath(f.ID, g.Gen)
			fi, err := os.Stat(cp)
			if err != nil {
				if g.CopyOffset > 0 && f.State != "copy_mismatch" && g.Gen == f.CurGen && st.ArchivedTo(f.ID, g.Gen) < g.CopyOffset {
					add("file %d gen %d: copy missing with committed offset %d", f.ID, g.Gen, g.CopyOffset)
				}
				continue
			}
			current := g.Gen == f.CurGen
			if current && f.State != "copy_mismatch" && fi.Size() != g.CopyOffset && st.ArchivedTo(f.ID, g.Gen) == 0 {
				add("file %d gen %d: copy length %d != committed offset %d", f.ID, g.Gen, fi.Size(), g.CopyOffset)
			}
			if !current && fi.Size() > g.CopyOffset {
				add("file %d gen %d: frozen copy length %d > committed offset %d", f.ID, g.Gen, fi.Size(), g.CopyOffset)
			}
			if current && f.State != "copy_mismatch" && st.ArchivedTo(f.ID, g.Gen) == 0 {
				if msg := originalPrefix(st, f, cp, g.CopyOffset); msg != "" {
					add("file %d gen %d: %s", f.ID, g.Gen, msg)
				}
			}
			if o.ArchiveCompare != nil {
				if err := o.ArchiveCompare(f.ID, g.Gen, cp); err != nil {
					add("file %d gen %d: archive compare: %v", f.ID, g.Gen, err)
				}
			}
		}
	}

	// No orphan temporary files.
	_ = filepath.WalkDir(st.Dir(), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".tmp") || strings.HasSuffix(p, ".corrupt-tmp")) {
			add("orphan temporary file %s", p)
		}
		return nil
	})

	// Journal replay equals the database's observations; the torn-journal
	// delta is the only permitted difference (records after the last intact
	// sequence were never applied, because the journal is fsynced first).
	jp := filepath.Join(st.Dir(), store.JournalName)
	delta, err := store.JournalDelta(jp)
	if err != nil {
		add("journal delta: %v", err)
	} else if st.LastSeq() > delta.LastGoodSeq {
		add("database applied sequence %d beyond the last intact journal sequence %d", st.LastSeq(), delta.LastGoodSeq)
	}
	replay, rerr := store.ReplayDump(jp)
	live, lerr := st.Dump()
	switch {
	case rerr != nil || lerr != nil:
		add("dump: replay=%v live=%v", rerr, lerr)
	case replay != live:
		add("journal replay differs from database observations:\n--- replay\n%s\n--- database\n%s", replay, live)
	}

	// The quarantine directory is covered: after a test, every removal has
	// either unlinked its quarantined files or restored them.
	if o.ClaudeDir != "" && !o.AllowQuarantine {
		_ = filepath.WalkDir(o.ClaudeDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() || d.Name() != quarantineDir {
				return nil
			}
			if ents, err := os.ReadDir(p); err == nil {
				for _, e := range ents {
					add("stray quarantine file %s", filepath.Join(p, e.Name()))
				}
			}
			return filepath.SkipDir
		})
	}

	if o.ClaudeBefore != nil && !o.AllowClaudeChange {
		after, err := SnapshotDir(o.ClaudeDir)
		if err != nil {
			add("snapshot Claude dir: %v", err)
		} else if d := o.ClaudeBefore.Diff(after); d != "" {
			add("the Claude directory changed: %s", d)
		}
	}
	return out
}

func originalPrefix(st *store.Store, f store.File, cp string, off int64) string {
	paths, _ := st.CurrentPaths(f.ID)
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() < off {
			continue
		}
		a, err := os.ReadFile(cp)
		if err != nil {
			return "reading copy: " + err.Error()
		}
		of, err := os.Open(p)
		if err != nil {
			continue
		}
		b := make([]byte, off)
		_, err = io.ReadFull(of, b)
		_ = of.Close()
		if err != nil {
			continue
		}
		if !bytes.Equal(a[:off], b) {
			return "copy bytes differ from the original's prefix at " + p
		}
		return ""
	}
	return ""
}

// DirSnapshot records paths, sizes, modification times and inodes of a tree.
type DirSnapshot struct{ entries map[string]string }

// SnapshotDir snapshots dir without following links.
func SnapshotDir(dir string) (*DirSnapshot, error) {
	s := &DirSnapshot{entries: map[string]string{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		s.entries[p] = fmt.Sprintf("size=%d mtime=%d mode=%v ino=%s", fi.Size(), fi.ModTime().UnixNano(), fi.Mode(), inodeOf(fi))
		return nil
	})
	return s, err
}

// Diff describes the differences from other ("" when equal).
func (s *DirSnapshot) Diff(other *DirSnapshot) string {
	var d []string
	for p, v := range s.entries {
		if w, ok := other.entries[p]; !ok {
			d = append(d, "removed "+p)
		} else if w != v {
			d = append(d, "modified "+p)
		}
	}
	for p := range other.entries {
		if _, ok := s.entries[p]; !ok {
			d = append(d, "added "+p)
		}
	}
	sort.Strings(d)
	return strings.Join(d, "; ")
}
