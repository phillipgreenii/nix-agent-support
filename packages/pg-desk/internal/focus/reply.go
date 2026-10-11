package focus

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

// The reply grammar of `focus select` (spec section 7.2 step 2).
//
// A reply is a list of tokens separated by whitespace or commas (so CRLF, tabs
// and a mix of separators all work):
//
//	ok        accept; may be combined with any other token
//	-<key>    strike the entity
//	+<key>    force-pull a candidate, or hand-add a key that is no candidate
//	cap=N     re-cap; N is a positive integer; at most once
//
// Keywords are case-insensitive. An exact repeat of ok, +<key> or -<key> is
// harmless and collapses to one; cap= repeated is a usage error, as are an
// EMPTY reply, an unrecognized token, a bad cap value and a conflicting pair
// (-X and +X) for one key. Parsing is atomic: it returns either a complete
// Reply or an error and a zero Reply, and nothing is applied before it
// returns.
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing; it is pure.

// ErrUsage is the sentinel every usage problem of this package wraps (match
// it with errors.Is). The verbs map it to the scaffold's focusUsageError
// (exit 1, no state changed).
var ErrUsage = errors.New("focus: usage error")

// UsageError is a usage problem with a one-line remedy. It matches ErrUsage.
type UsageError struct {
	Msg    string
	Remedy string
}

// Error is the message and the remedy in one line, `msg; remedy`, the shape of
// the scaffold's focusUsageError.
func (e *UsageError) Error() string {
	if e.Remedy == "" {
		return e.Msg
	}
	return e.Msg + "; " + e.Remedy
}

// Is reports a match with ErrUsage.
func (e *UsageError) Is(target error) bool { return target == ErrUsage }

func usageErr(remedy, format string, args ...any) error {
	return &UsageError{Msg: fmt.Sprintf(format, args...), Remedy: remedy}
}

// maxReplyBytes bounds how much of a reply is read: a reply is a few tokens.
const maxReplyBytes = 1 << 20

// Reply is a parsed reply. Plus and Minus hold the RAW keys in the order
// written, without duplicates; they are resolved to keys by ParseKey (the
// grammar does not know the configured repository). Ok records that the
// reply said ok; it changes nothing in the final set (the draft's proposals
// are locked either way) and exists so an empty reply is distinguishable
// from an accepting one.
type Reply struct {
	Ok          bool
	Plus, Minus []string
	Cap         *int
}

const (
	replyRemedy    = "reply ok to accept, or -<key>, +<key> and cap=N"
	tokenOK        = "ok"
	tokenCapPrefix = "cap="
)

// ParseReply parses a reply read from r. Every error is an ErrUsage and
// carries a one-line remedy, except an I/O error of r itself.
func ParseReply(r io.Reader) (Reply, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxReplyBytes+1))
	if err != nil {
		return Reply{}, fmt.Errorf("focus: read reply: %w", err)
	}
	if len(raw) > maxReplyBytes {
		return Reply{}, usageErr(replyRemedy, "reply is longer than %d bytes", maxReplyBytes)
	}
	tokens := strings.FieldsFunc(string(raw), func(c rune) bool {
		return c == ',' || unicode.IsSpace(c)
	})
	if len(tokens) == 0 {
		return Reply{}, usageErr("reply ok to accept", "reply is empty")
	}
	var out Reply
	seenPlus := map[string]bool{}
	seenMinus := map[string]bool{}
	for _, tok := range tokens {
		switch {
		case strings.EqualFold(tok, tokenOK):
			out.Ok = true
		case len(tok) >= len(tokenCapPrefix) && strings.EqualFold(tok[:len(tokenCapPrefix)], tokenCapPrefix):
			if out.Cap != nil {
				return Reply{}, usageErr("give cap=N once", "cap= appears more than once")
			}
			n, err := parseCapValue(tok[len(tokenCapPrefix):])
			if err != nil {
				return Reply{}, usageErr("write cap=N with N a positive integer", "%v in %q", err, tok)
			}
			out.Cap = &n
		case tok[0] == '+':
			key := tok[1:]
			if key == "" {
				return Reply{}, usageErr("write +<key>", "token %q names no key", tok)
			}
			if !seenPlus[key] {
				seenPlus[key] = true
				out.Plus = append(out.Plus, key)
			}
		case tok[0] == '-':
			key := tok[1:]
			if key == "" {
				return Reply{}, usageErr("write -<key>", "token %q names no key", tok)
			}
			if !seenMinus[key] {
				seenMinus[key] = true
				out.Minus = append(out.Minus, key)
			}
		default:
			return Reply{}, usageErr(replyRemedy, "unrecognized token %q", tok)
		}
	}
	for _, k := range out.Plus {
		if seenMinus[k] {
			return Reply{}, usageErr("drop one of them", "conflicting tokens -%s and +%s", k, k)
		}
	}
	return out, nil
}

// parseCapValue parses the N of cap=N: ASCII digits only, at least 1.
func parseCapValue(s string) (int, error) {
	if s == "" {
		return 0, errors.New("cap value is empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("cap value is not a positive integer")
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errors.New("cap value is too large")
	}
	if n < 1 {
		return 0, errors.New("cap value is not a positive integer")
	}
	return n, nil
}
