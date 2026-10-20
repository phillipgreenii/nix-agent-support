package collector

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// feedSource is one sources[] row of a changes answer.
type feedSource struct {
	Backend string `json:"backend"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	NextSeq int64  `json:"next_seq"`
}

// feedChange is one changes[] row of either instance: a pr row's entity has id
// and updated_at, a ci row's has pr_id and rollup.
type feedChange struct {
	Change string   `json:"change"`
	Seq    int64    `json:"seq"`
	Kinds  []string `json:"kinds"`
	Fields []struct {
		Name string `json:"name"`
	} `json:"fields"`
	Entity struct {
		ID        string `json:"id"`
		PRID      string `json:"pr_id"`
		UpdatedAt string `json:"updated_at"`
		Rollup    string `json:"rollup"`
	} `json:"entity"`
}

// feedResult is the result of a changes answer (the same envelope for the pr
// and the ci instance).
type feedResult struct {
	Sources []feedSource `json:"sources"`
	Changes []feedChange `json:"changes"`
}

// answerError is an error answer: the daemon's own {code, message}, or a
// failure of the call itself (code "client").
type answerError struct{ Code, Message string }

func (e *answerError) text() string { return e.Code + ": " + e.Message }

// cursorExpired reports the one error answer the collector recovers from: the
// key's position fell outside retention.
func (e *answerError) cursorExpired() bool {
	return e.Code == "invalid_argument" && strings.HasPrefix(e.Message, "cursor_expired:")
}

// clientCodeLocal is the code of an error that never came from the daemon.
const clientCodeLocal = "client"

// parseChanges decodes one client call's answer. A non-zero exit, an empty or
// undecodable stdout and an answer with neither result nor error are all error
// answers; a daemon error answer keeps its own code and message.
func parseChanges(stdout, stderr string, exit int) (feedResult, *answerError) {
	var out feedResult
	if strings.TrimSpace(stdout) == "" {
		msg := firstLine(stderr)
		if msg == "" {
			msg = fmt.Sprintf("empty answer (exit %d)", exit)
		}
		return out, &answerError{Code: clientCodeLocal, Message: msg}
	}
	var w struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &w); err != nil {
		return out, &answerError{Code: clientCodeLocal, Message: "decode answer: " + err.Error()}
	}
	if w.Error != nil {
		return out, &answerError{Code: w.Error.Code, Message: w.Error.Message}
	}
	if len(w.Result) == 0 {
		return out, &answerError{Code: clientCodeLocal, Message: "the answer has neither a result nor an error"}
	}
	if exit != 0 {
		return out, &answerError{Code: clientCodeLocal, Message: fmt.Sprintf("the client exited %d with a result", exit)}
	}
	if err := json.Unmarshal(w.Result, &out); err != nil {
		return out, &answerError{Code: clientCodeLocal, Message: "decode result: " + err.Error()}
	}
	return out, nil
}

var (
	refRE   = regexp.MustCompile(`[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(#[0-9]+)?`)
	atRE    = regexp.MustCompile(`@[A-Za-z0-9_-]+`)
	pathRE  = regexp.MustCompile(`/[A-Za-z0-9_.@+-]+(/[A-Za-z0-9_.@+-]+)+`)
	spaceRE = regexp.MustCompile(`\s+`)
)

// Scrub makes an error text safe to leave the scratch directory: known
// literals (login, repo slug) and every owner/repo, #number, @handle and path
// shaped token are replaced, whitespace collapsed and the text truncated.
func Scrub(s string, literals []string) string {
	s = pathRE.ReplaceAllString(s, "<path>")
	for _, l := range literals {
		if l != "" {
			s = strings.ReplaceAll(s, l, "<redacted>")
		}
	}
	s = refRE.ReplaceAllString(s, "<ref>")
	s = atRE.ReplaceAllString(s, "<@>")
	s = spaceRE.ReplaceAllString(strings.TrimSpace(s), " ")
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}

func parseKinds(raw string) []string {
	var out []string
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}
