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

	perID := map[string]int64{}
	var noID int64
	sc := newTranscriptScanner(f)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // tolerate non-event lines
		}
		if ev.Type != "assistant" {
			continue
		}
		out := int64(ev.Message.Usage.OutputTokens)
		if ev.Message.ID == "" {
			noID += out
			continue
		}
		if out > perID[ev.Message.ID] {
			perID[ev.Message.ID] = out
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	total := noID
	for _, v := range perID {
		total += v
	}
	return total, nil
}
