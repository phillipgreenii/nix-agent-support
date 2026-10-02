package report

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Fingerprint identifies "the same failure again" for handlers that
// deduplicate. It is computed once by the wrapper, as "sha256:" and 64 hex
// digits.
//
// In ModeArgv it hashes each argv element followed by a NUL byte, then cwd and
// a NUL. In ModeStdin it hashes "stdin" and a NUL, cwd and a NUL, then context
// and a NUL; argv is ignored. The captured output is deliberately NOT an
// input, so a repeat with a different error message still matches. In ModeArgv
// the context is not an input either: it describes the caller's intent, not
// the failure.
func Fingerprint(mode Mode, argv []string, cwd, context string) string {
	var in strings.Builder
	if mode == ModeStdin {
		in.WriteString("stdin\x00")
		in.WriteString(cwd + "\x00")
		in.WriteString(context + "\x00")
	} else {
		for _, a := range argv {
			in.WriteString(a + "\x00")
		}
		in.WriteString(cwd + "\x00")
	}
	sum := sha256.Sum256([]byte(in.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}
