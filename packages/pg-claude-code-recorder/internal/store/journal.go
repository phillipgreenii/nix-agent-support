package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// readJournal parses the journal at path. It returns the intact records, the
// byte length of the intact prefix, and the size of the torn tail (a final
// line without its newline, or a line that is not valid JSON, and everything
// after it).
func readJournal(path string) (recs []Rec, goodLen int64, tornBytes int64, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, err
	}
	var off int64
	for off < int64(len(data)) {
		nl := bytes.IndexByte(data[off:], '\n')
		if nl < 0 {
			break
		}
		var r Rec
		if json.Unmarshal(data[off:off+int64(nl)], &r) != nil || r.T == "" {
			break
		}
		recs = append(recs, r)
		off += int64(nl) + 1
	}
	return recs, off, int64(len(data)) - off, nil
}

// TornJournalDelta describes the torn tail of a journal: the last intact
// sequence number and the number of bytes after it. A torn record was fsynced
// partially, so its database commit never happened (the journal is written and
// fsynced first); the delta is what recovery drops.
type TornJournalDelta struct {
	LastGoodSeq int64
	TornBytes   int64
}

// JournalDelta reads the journal at path without repairing it.
func JournalDelta(path string) (TornJournalDelta, error) {
	recs, _, torn, err := readJournal(path)
	if err != nil {
		return TornJournalDelta{}, err
	}
	var last int64
	if len(recs) > 0 {
		last = recs[len(recs)-1].Seq
	}
	return TornJournalDelta{LastGoodSeq: last, TornBytes: torn}, nil
}

// JournalFreeze is a consistent view of the journal and the index, both taken
// at one instant: no commit was in flight between reading the two.
type JournalFreeze struct {
	// Path is a private copy of the journal; Remove deletes it.
	Path string
	// LastSeq is the last sequence the index had applied.
	LastSeq int64
	// Live is the index's Dump.
	Live string
	dir  string
}

// Remove deletes the journal copy.
func (j JournalFreeze) Remove() { _ = os.RemoveAll(j.dir) }

// FreezeJournal copies the journal and reads the index's last sequence and
// Dump while holding the write lock. A commit writes the journal first and the
// index second, both under that lock, so reading either without it can see the
// journal ahead of the index (a false journal_differs_from_index on a live
// daemon). The copy is then checked and replayed outside the lock.
func (s *Store) FreezeJournal() (JournalFreeze, error) {
	dir, err := os.MkdirTemp("", "ccrec-freeze-")
	if err != nil {
		return JournalFreeze{}, err
	}
	j := JournalFreeze{dir: dir, Path: dir + "/" + JournalName}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.dir, JournalName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		j.Remove()
		return JournalFreeze{}, err
	}
	if err := os.WriteFile(j.Path, data, 0o600); err != nil {
		j.Remove()
		return JournalFreeze{}, err
	}
	j.LastSeq = s.lastSeq
	if j.Live, err = s.Dump(); err != nil {
		j.Remove()
		return JournalFreeze{}, err
	}
	return j, nil
}
