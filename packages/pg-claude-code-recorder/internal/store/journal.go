package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
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
