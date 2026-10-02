package claudetranscript

import (
	"encoding/json"
	"os"
)

// OutputTokens returns the total output_tokens the model produced in the
// transcript at path: the sum over DISTINCT assistant messages.
//
// A single assistant turn is written as one JSONL line per content block, all
// sharing one Message.ID and repeating the same usage (see Message.ID), so a
// naive sum over assistant lines over-counts a multi-block turn (measured on a
// real transcript, 2026-10-02: 687297 summed over lines vs 294537 summed over
// distinct ids). Within one id the largest output_tokens wins, which also
// absorbs a streamed partial line followed by the final one. A line with no id
// cannot be deduplicated and counts on its own. Non-assistant and unparseable
// lines are skipped. A missing or unreadable file is an error; an empty
// transcript is (0, nil).
func OutputTokens(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	var tally OutputTally
	sc := newTranscriptScanner(f)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // tolerate non-event lines
		}
		if ev.Type != "assistant" {
			continue
		}
		tally.Add(ev.Message.ID, ev.Message.Usage.OutputTokens)
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return tally.Total(), nil
}

// OutputTally accumulates output_tokens across assistant transcript lines
// incrementally, counting each distinct message id once (see OutputTokens for
// why a naive per-line sum over-counts). It is the single definition of that
// rule for callers that fold a transcript line by line (pa-monitor's
// incremental scanner) rather than reading a whole file. The zero value is
// ready to use.
type OutputTally struct {
	perID map[string]int64
	noID  int64
}

// Add folds one assistant line's output_tokens in. Within one id the largest
// value wins (absorbing a streamed partial line followed by the final one); an
// empty id cannot be deduplicated and counts on its own.
func (t *OutputTally) Add(messageID string, outputTokens int) {
	out := int64(outputTokens)
	if messageID == "" {
		t.noID += out
		return
	}
	if t.perID == nil {
		t.perID = map[string]int64{}
	}
	if out > t.perID[messageID] {
		t.perID[messageID] = out
	}
}

// Total returns the deduplicated output_tokens folded in so far.
func (t *OutputTally) Total() int64 {
	total := t.noID
	for _, v := range t.perID {
		total += v
	}
	return total
}
