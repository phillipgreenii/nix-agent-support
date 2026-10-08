package event

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// MaxEventBytes is the longest encoded event line, without its newline.
const MaxEventBytes = 262144

// ErrTooLarge reports text or an encoded event longer than MaxEventBytes.
var ErrTooLarge = errors.New("event text is too large: an event line is at most 262144 bytes")

// ErrInvalidUTF8 reports text that is not valid UTF-8. Such text is refused,
// never rewritten: json.Marshal would store the replacement character in its
// place, and a stored note must be exactly what the operator wrote.
var ErrInvalidUTF8 = errors.New("event text is not valid UTF-8")

// ValidText is the boundary check for free text and for every key or value a
// client supplies: each string MUST be valid UTF-8, and together they MUST fit
// in one event. It returns ErrInvalidUTF8 or ErrTooLarge unwrapped.
func ValidText(strs ...string) error {
	total := 0
	for _, s := range strs {
		if !utf8.ValidString(s) {
			return ErrInvalidUTF8
		}
		total += len(s)
		if total > MaxEventBytes {
			return ErrTooLarge
		}
	}
	return nil
}

// ValidReason checks a skip or override reason and returns it without its
// surrounding whitespace. Blank means strings.TrimSpace(s) == "" (Unicode
// white space, so U+00A0 and U+3000 count) and is an error; so are invalid
// UTF-8 and text over the size limit.
func ValidReason(s string) (string, error) {
	if err := ValidText(s); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", errors.New("a reason MUST NOT be blank: empty or only white space")
	}
	return trimmed, nil
}
