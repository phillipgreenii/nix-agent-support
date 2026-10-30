package settle

// Extract writes a Conversation back into the Claude directory so that
// claude --resume works, or into another directory with --to. The rules, from
// the spike on what claude --resume needs:
//
//   - refuse when the session id exists under any project directory, or a live
//     Process holds it;
//   - restore byte-exact to the last recorded path;
//   - set each restored file's modification time to now, because Claude's own
//     cleanup looks only at modification time;
//   - register the expected paths and hashes first, so the copier attaches to the
//     existing copies instead of copying them again. An extract counts as a
//     write: the restored files reset the Conversation's quiet age.
//
// A crash leaves an "extracting" stage whose list names the files; running the
// extract again deletes those partial files and starts over.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// ExtractResult is what Extract restored.
type ExtractResult struct {
	Session string   `json:"session"`
	Paths   []string `json:"paths"`
	// Resume is the line that resumes the Conversation ("" for --to).
	Resume string `json:"resume,omitempty"`
}

// ErrBadTarget is returned for a --to directory inside the Claude directory.
var ErrBadTarget = errors.New("--to must not be inside the Claude directory")

type extractEntry struct {
	File int64  `json:"file"`
	Rel  string `json:"rel"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
}

// sessionExists reports whether the session id exists under any project
// directory of the Claude directory.
func (s *Settler) sessionExists(session string) (string, bool) {
	proj := filepath.Join(s.cfg.ClaudeDir, "projects")
	ents, err := os.ReadDir(proj)
	if err != nil {
		return "", false
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		for _, name := range []string{session + ".jsonl", session} {
			p := filepath.Join(proj, e.Name(), name)
			if _, err := os.Lstat(p); err == nil {
				return p, true
			}
		}
	}
	return "", false
}

// Extract restores the Conversation. to is "" for the Claude directory.
func (s *Settler) Extract(session, to string) (ExtractResult, error) {
	s.run.Lock()
	defer s.run.Unlock()
	res := ExtractResult{Session: session, Paths: []string{}}
	if !uuidRe.MatchString(session) {
		return res, store.ErrNotFound
	}
	settled, ok, err := s.st.SettledOf(session)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, store.ErrNotFound
	}
	var m store.Manifest
	if err := json.Unmarshal(settled.Manifest, &m); err != nil {
		return res, fmt.Errorf("manifest of %s: %w", session, err)
	}
	live, err := s.cfg.Live()
	if err != nil {
		return res, err
	}
	if live[session] {
		return res, ErrHeldByLive
	}
	now := s.cfg.Clock.Now()
	row, hasRow, err := s.st.Stage(session)
	if err != nil {
		return res, err
	}
	resuming := hasRow && row.Stage == store.StageExtracting
	root := s.cfg.ClaudeDir
	if to != "" {
		abs, err := filepath.Abs(to)
		if err != nil {
			return res, err
		}
		rc, rt := realOrSame(root), realOrSame(abs)
		if rt == rc || strings.HasPrefix(rt, rc+string(filepath.Separator)) {
			return res, ErrBadTarget
		}
		root = abs
	} else if !resuming {
		if p, exists := s.sessionExists(session); exists {
			return res, fmt.Errorf("%w: %s", ErrTargetExists, p)
		}
	}
	if len(m.Files) == 0 {
		return res, store.ErrNotFound
	}
	for _, mf := range m.Files {
		p := filepath.Join(root, mf.Path)
		if _, err := os.Lstat(p); err == nil {
			if to == "" && resuming {
				if err := os.Remove(p); err != nil { // a partial file of the unfinished extract
					return res, err
				}
				continue
			}
			return res, fmt.Errorf("%w: %s", ErrTargetExists, p)
		}
	}
	if to == "" {
		list := make([]extractEntry, 0, len(m.Files))
		for _, mf := range m.Files {
			list = append(list, extractEntry{File: mf.File, Rel: mf.Path, Size: mf.Size, Hash: mf.Hash})
		}
		b, _ := json.Marshal(list)
		if err := s.st.CommitFull(&store.Rec{T: store.TStage, At: now.UnixNano(), Session: session, State: store.StageExtracting, Val: string(b)}); err != nil {
			return res, err
		}
		faults.At("extract.after-register")
	}
	cfs := s.cfs()
	err = s.cfg.Copier.WithLock(func() error {
		for i, mf := range m.Files {
			final := filepath.Join(root, mf.Path)
			if err := cfs.MkdirAll(filepath.Dir(final), 0o700); err != nil {
				return err
			}
			tmp := final + ".tmp.pgrec"
			if err := s.writeRestored(mf, tmp, now); err != nil {
				_ = os.Remove(tmp)
				return err
			}
			if i == 0 {
				faults.At("extract.after-mtime")
			}
			if to == "" {
				id, ok := copier.IdentityOfFile(tmp)
				if !ok {
					_ = os.Remove(tmp)
					return fmt.Errorf("cannot read the identity of %s", tmp)
				}
				recs := []*store.Rec{
					{T: store.TIdent, At: now.UnixNano(), File: mf.File, Dev: id.Dev, Ino: id.Ino, Birth: id.Birth, FirstHash: copier.FirstHashOf(tmp)},
					{T: store.TPath, At: now.UnixNano(), File: mf.File, Path: final},
				}
				if err := s.st.Commit(recs...); err != nil {
					_ = os.Remove(tmp)
					return err
				}
				if i == 0 {
					faults.At("extract.after-path")
				}
			}
			// An exclusive rename: a link, then the temporary name goes.
			if err := os.Link(tmp, final); err != nil {
				_ = os.Remove(tmp)
				if errors.Is(err, fs.ErrExist) {
					return fmt.Errorf("%w: %s", ErrTargetExists, final)
				}
				return err
			}
			if err := cfs.Remove(tmp); err != nil {
				return err
			}
			if err := cfs.SyncDir(filepath.Dir(final)); err != nil {
				return err
			}
			res.Paths = append(res.Paths, final)
			faults.At("extract.after-partial")
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	if to == "" {
		if err := s.st.CommitFull(&store.Rec{T: store.TStage, At: now.UnixNano(), Session: session, State: "", Reason: "extracted"}); err != nil {
			return res, err
		}
		s.setReason(session, "extract")
		res.Resume = "claude --resume " + session
	}
	s.cfg.Obs.Event(obs.EvExtracted, "session", session, "files", fmt.Sprint(len(res.Paths)), "to", to)
	return res, nil
}

func realOrSame(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	// A target that does not exist yet: resolve its nearest existing parent.
	dir, base := filepath.Split(filepath.Clean(p))
	if dir == "" || dir == p {
		return p
	}
	return filepath.Join(realOrSame(filepath.Clean(dir)), base)
}

// writeRestored writes one file from the archive to tmp, verifies its hash
// against the manifest, sets its modification time to now and flushes it.
func (s *Settler) writeRestored(mf store.ManifestFile, tmp string, now time.Time) error {
	ptr, err := s.st.OpenPointer(mf.File, mf.Gen)
	if err != nil {
		return err
	}
	defer func() { _ = ptr.Close() }()
	_ = os.Remove(tmp) // a leftover of an unfinished extract
	f, err := s.cfs().OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := xxhash.New()
	buf := make([]byte, 1<<20)
	var off int64
	for off < mf.Size {
		n := int64(len(buf))
		if mf.Size-off < n {
			n = mf.Size - off
		}
		got, rerr := ptr.ReadAt(buf[:n], off)
		if int64(got) != n {
			_ = f.Close()
			if rerr == nil {
				rerr = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("reading %s from the archive: %w", mf.Path, rerr)
		}
		_, _ = h.Write(buf[:n])
		if _, err := f.Write(buf[:n]); err != nil {
			_ = f.Close()
			return err
		}
		off += n
	}
	if want := hexHash(h.Sum64()); mf.Hash != "" && want != mf.Hash {
		_ = f.Close()
		return fmt.Errorf("%s: the archive's bytes hash to %s, the manifest says %s", mf.Path, want, mf.Hash)
	}
	if err := fsx.FullSync(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(tmp, now, now)
}
