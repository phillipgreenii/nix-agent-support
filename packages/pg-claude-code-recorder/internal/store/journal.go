package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	recs, goodLen, tornBytes = decodeJournal(data)
	return recs, goodLen, tornBytes, nil
}

// journalDecodes counts decodeJournal calls (a test reads it).
var journalDecodes atomic.Int64

// decodeJournal parses journal bytes the way readJournal describes.
func decodeJournal(data []byte) (recs []Rec, goodLen int64, tornBytes int64) {
	journalDecodes.Add(1)
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
	return recs, off, int64(len(data)) - off
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
// at one instant: no commit was in flight between reading the two. It holds
// the journal's bytes in memory and decodes them once, for both the delta and
// the replay.
type JournalFreeze struct {
	// LastSeq is the last sequence the index had applied.
	LastSeq int64
	// Live is the index's Dump.
	Live string

	data []byte
	once sync.Once
	dec  decodedJournal
}

// decodedJournal is a journal parsed once.
type decodedJournal struct {
	recs []Rec
	good int64
	torn int64
}

func (j *JournalFreeze) decoded() *decodedJournal {
	j.once.Do(func() { j.dec.recs, j.dec.good, j.dec.torn = decodeJournal(j.data) })
	return &j.dec
}

// FreezeJournal reads the journal and the index's last sequence and Dump while
// holding the write lock. A commit writes the journal first and the index
// second, both under that lock, so reading either without it can see the
// journal ahead of the index (a false journal_differs_from_index on a live
// daemon). The bytes are then checked and replayed outside the lock.
func (s *Store) FreezeJournal() (*JournalFreeze, error) {
	j := &JournalFreeze{}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.dir, JournalName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	j.data = data
	j.LastSeq = s.lastSeq
	if j.Live, err = s.Dump(); err != nil {
		return nil, err
	}
	return j, nil
}

// Delta describes the frozen journal's torn tail, like JournalDelta.
func (j *JournalFreeze) Delta() TornJournalDelta {
	d := j.decoded()
	var last int64
	if len(d.recs) > 0 {
		last = d.recs[len(d.recs)-1].Seq
	}
	return TornJournalDelta{LastGoodSeq: last, TornBytes: d.torn}
}

// Replay replays the frozen journal into a scratch state directory and returns
// its Dump, like ReplayDump, without reading or decoding the journal again.
func (j *JournalFreeze) Replay() (string, error) { return j.ReplayCtx(context.Background()) }

// ReplayCtx is Replay with the scratch store opened and closed under ctx.
func (j *JournalFreeze) ReplayCtx(ctx context.Context) (string, error) {
	return replayDump(ctx, j.data, j.decoded())
}
