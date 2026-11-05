package settle

// Doctor compares every copy with its Claude file by full hash, checks the
// journal against the index and the settled archives against their manifests.
// A full-hash mismatch starts a new generation from byte 0 (a reset entry
// follows) and keeps the old one. Its result is written to doctor.json in the
// state directory, which the daemon exports as doctor_last_run_timestamp and
// doctor_mismatch_files.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// DoctorFile is the name of the result file in the state directory.
const DoctorFile = "doctor.json"

// Mismatch kinds.
const (
	MismatchCopy     = "copy_differs_from_original"
	MismatchSnapshot = "snapshot_differs_from_original"
	MismatchJournal  = "journal_differs_from_index"
	MismatchManifest = "archive_differs_from_manifest"
)

// DoctorMismatch is one thing doctor found wrong.
type DoctorMismatch struct {
	Kind    string `json:"kind"`
	File    int64  `json:"file,omitempty"`
	Session string `json:"session,omitempty"`
	Path    string `json:"path,omitempty"`
	Detail  string `json:"detail"`
	// Repaired is true when doctor started a new generation for the file.
	Repaired bool `json:"repaired,omitempty"`
}

// DoctorResult is the result file's content and the doctor answer.
type DoctorResult struct {
	AtNS          int64            `json:"at_ns"`
	FilesChecked  int              `json:"files_checked"`
	ArchivesCheck int              `json:"archives_checked"`
	MismatchFiles int              `json:"mismatch_files"`
	Mismatches    []DoctorMismatch `json:"mismatches"`
}

// doctorFull is the old synchronous comparison (doctor.bytes_per_slice = 0): it
// holds the copier lock across every file's full hash.
func (s *Settler) doctorFull() (DoctorResult, error) {
	now := s.cfg.Clock.Now()
	res := DoctorResult{AtNS: now.UnixNano(), Mismatches: []DoctorMismatch{}}
	bad := map[int64]bool{}
	add := func(m DoctorMismatch) {
		res.Mismatches = append(res.Mismatches, m)
		if m.File != 0 {
			bad[m.File] = true
		} else {
			bad[-int64(len(res.Mismatches))] = true
		}
	}
	files, err := s.st.AllFiles()
	if err != nil {
		return res, err
	}
	err = s.cfg.Copier.WithLock(func() error {
		for _, f := range files {
			orig := s.cfg.Copier.ReadOriginalPath(f)
			if orig == "" {
				continue
			}
			switch f.Mode {
			case "append":
				if f.CurGen == 0 {
					continue
				}
				g, err := s.st.Generation(f.ID, f.CurGen)
				if err != nil {
					return err
				}
				res.FilesChecked++
				ok, detail, err := s.prefixMatches(f, g, orig)
				if err != nil {
					return err
				}
				if ok {
					continue
				}
				m := DoctorMismatch{Kind: MismatchCopy, File: f.ID, Path: orig, Detail: detail}
				if err := s.cfg.Copier.NewGenerationLocked(f, orig, "full_hash_mismatch"); err != nil {
					m.Detail += "; new generation failed: " + err.Error()
				} else {
					m.Repaired = true
				}
				add(m)
			case "snapshot":
				sn, err := s.st.LatestSnapshot(f.ID)
				if err != nil {
					continue
				}
				res.FilesChecked++
				h, size, err := hashFile(orig)
				if err != nil {
					continue
				}
				if (h != sn.Hash || size != sn.Size) && !s.lastSeen(f).Before(s.mtimeOf(orig)) {
					add(DoctorMismatch{Kind: MismatchSnapshot, File: f.ID, Path: orig, Detail: fmt.Sprintf("stored %d bytes, original %d", sn.Size, size)})
				}
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	s.doctorJournal(add)
	if err := s.doctorArchives(&res, add); err != nil {
		return res, err
	}
	res.MismatchFiles = len(bad)
	sort.SliceStable(res.Mismatches, func(i, j int) bool { return res.Mismatches[i].File < res.Mismatches[j].File })
	if err := s.writeDoctor(res); err != nil {
		return res, err
	}
	s.cfg.Obs.Reg.Set(obs.MDoctorLastRun, nil, float64(now.UnixNano())/1e9)
	s.cfg.Obs.Reg.Set(obs.MDoctorMismatchFiles, nil, float64(res.MismatchFiles))
	s.markRemovalGauges()
	s.cfg.Obs.Event(obs.EvDoctorDone, "files", fmt.Sprint(res.FilesChecked), "mismatch_files", fmt.Sprint(res.MismatchFiles))
	return res, nil
}

func (s *Settler) mtimeOf(p string) time.Time {
	t, err := s.cfg.Mtime(p)
	if err != nil {
		return time.Time{}
	}
	return t
}

// prefixMatches compares the copy's committed bytes with the same-length prefix
// of the original (the original may be longer while the copier is behind).
func (s *Settler) prefixMatches(f store.File, g store.Generation, orig string) (bool, string, error) {
	if g.CopyOffset == 0 {
		return true, "", nil
	}
	of, err := os.Open(orig)
	if err != nil {
		return true, "", nil
	}
	defer func() { _ = of.Close() }()
	fi, err := of.Stat()
	if err != nil {
		return true, "", nil
	}
	if fi.Size() < g.CopyOffset {
		return false, fmt.Sprintf("the original has %d bytes, the copy %d", fi.Size(), g.CopyOffset), nil
	}
	ptr, err := s.st.OpenPointer(f.ID, g.Gen)
	if err != nil {
		return false, "", err
	}
	defer func() { _ = ptr.Close() }()
	ch, err := hashReader(ptr, g.CopyOffset)
	if err != nil {
		return false, "", err
	}
	oh, err := hashReader(of, g.CopyOffset)
	if err != nil {
		return true, "", nil
	}
	if ch != oh {
		return false, fmt.Sprintf("the first %d bytes differ", g.CopyOffset), nil
	}
	return true, "", nil
}

// doctorJournal checks the journal against the index: the index never applied a
// record beyond the journal's last intact sequence, and a replay of the journal
// equals the index's observations.
func (s *Settler) doctorJournal(add func(DoctorMismatch)) {
	// A frozen view: reading the journal and the index at different instants
	// races a commit in flight (journal first, index second).
	fz, err := s.st.FreezeJournal()
	if err != nil {
		add(DoctorMismatch{Kind: MismatchJournal, Detail: "reading the journal: " + err.Error()})
		return
	}
	delta := fz.Delta()
	if fz.LastSeq > delta.LastGoodSeq {
		add(DoctorMismatch{Kind: MismatchJournal, Detail: fmt.Sprintf("the index applied sequence %d, the journal's last intact sequence is %d", fz.LastSeq, delta.LastGoodSeq)})
		return
	}
	replay, rerr := fz.Replay()
	switch {
	case rerr != nil:
		add(DoctorMismatch{Kind: MismatchJournal, Detail: fmt.Sprintf("dump: replay=%v", rerr)})
	case replay != fz.Live:
		add(DoctorMismatch{Kind: MismatchJournal, Detail: "a replay of the journal differs from the index"})
	}
}

// doctorArchives checks each settled Conversation's archive against its
// manifest: the manifest file on disk equals the committed one and every file's
// bytes, read through the archive, hash to the manifest's hash.
func (s *Settler) doctorArchives(res *DoctorResult, add func(DoctorMismatch)) error {
	sessions, err := s.settledSessions()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		checked, _ := s.doctorArchive(session, add)
		if checked {
			res.ArchivesCheck++
		}
	}
	return nil
}

// settledSessions lists the settled Conversations in session order.
func (s *Settler) settledSessions() ([]string, error) {
	rows, err := s.st.Read().Query(`SELECT session FROM settled ORDER BY session`)
	if err != nil {
		return nil, err
	}
	var sessions []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			_ = rows.Close()
			return nil, err
		}
		sessions = append(sessions, x)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return sessions, nil
}

// doctorArchive checks one Conversation's archive; checked is false when the
// Conversation is not settled, and hashed is the bytes hashed.
func (s *Settler) doctorArchive(session string, add func(DoctorMismatch)) (checked bool, hashed int64) {
	st, ok, err := s.st.SettledOf(session)
	if err != nil || !ok {
		return false, 0
	}
	disk, err := os.ReadFile(filepath.Join(s.st.SegDir(session), "manifest.json"))
	switch {
	case err != nil:
		add(DoctorMismatch{Kind: MismatchManifest, Session: session, Detail: "manifest.json: " + err.Error()})
	case !bytes.Equal(bytes.TrimSpace(disk), bytes.TrimSpace(st.Manifest)):
		add(DoctorMismatch{Kind: MismatchManifest, Session: session, Detail: "manifest.json differs from the committed manifest"})
	}
	var m store.Manifest
	if json.Unmarshal(st.Manifest, &m) != nil {
		add(DoctorMismatch{Kind: MismatchManifest, Session: session, Detail: "the committed manifest is not valid JSON"})
		return true, 0
	}
	for _, mf := range m.Files {
		if mf.Hash == "" {
			continue
		}
		ptr, err := s.st.OpenPointer(mf.File, mf.Gen)
		if err != nil {
			add(DoctorMismatch{Kind: MismatchManifest, File: mf.File, Session: session, Path: mf.Path, Detail: "opening the stored bytes: " + err.Error()})
			continue
		}
		h, herr := hashReader(ptr, mf.Size)
		_ = ptr.Close()
		hashed += mf.Size
		if herr != nil && !errors.Is(herr, io.EOF) {
			add(DoctorMismatch{Kind: MismatchManifest, File: mf.File, Session: session, Path: mf.Path, Detail: "reading the stored bytes: " + herr.Error()})
			continue
		}
		if hexHash(h) != mf.Hash {
			add(DoctorMismatch{Kind: MismatchManifest, File: mf.File, Session: session, Path: mf.Path, Detail: "the stored bytes hash to " + hexHash(h) + ", the manifest says " + mf.Hash})
		}
	}
	return true, hashed
}

// writeDoctor writes the result file atomically.
func (s *Settler) writeDoctor(res DoctorResult) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(s.st.Dir(), DoctorFile)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// LoadDoctor reads the result file of dir (ok is false when none exists).
func LoadDoctor(dir string) (DoctorResult, bool) {
	var r DoctorResult
	b, err := os.ReadFile(filepath.Join(dir, DoctorFile))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	return r, true
}
