package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-wrap/internal/fail"
	"github.com/phillipgreenii/pg-wrap/internal/spec"
)

// readEnvNames reads a NAME=value environment dump (INV-ENTRY-6) and returns
// the distinct variable names, sorted. Values are discarded as each line is
// read: nothing downstream, and no message, ever holds one. A blank line is
// skipped; a line with no "=", an empty name or a NUL in the name is refused,
// naming only its line number and never its text, because that text may be
// a value.
func readEnvNames(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// The path is the operator's own argument. os's message holds only the
		// path and the reason, never file content.
		return nil, fail.New(fail.StageEnv, "--env-from: cannot read %s: %v", path, unwrapPathError(err))
	}
	seen := map[string]bool{}
	var names []string
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		if !ok || name == "" || strings.ContainsRune(name, 0) {
			return nil, fail.New(fail.StageEnv, "--env-from: %s line %d is not NAME=value (its text is not shown)", path, i+1)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// unwrapPathError drops the "open <path>: " prefix of an *fs.PathError, so the
// path is not printed twice.
func unwrapPathError(err error) error {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err
	}
	return err
}

// classifyEnv splits names into those the spec's starting environment would
// carry and those it would drop. It runs the real buildEnv over name-only
// entries, so it can never disagree with a run. PATH under strict_env is
// dropped: the minimal environment sets its own fixed PATH, so the dump's PATH
// is not carried.
func classifyEnv(s *spec.Spec, names []string) (kept, dropped []string, err error) {
	environ := make([]string, len(names))
	for i, n := range names {
		environ[i] = n + "="
	}
	out, err := buildEnv(s, environ)
	if err != nil {
		return nil, nil, err
	}
	carried := map[string]bool{}
	for _, kv := range out {
		if n, _, ok := strings.Cut(kv, "="); ok {
			carried[n] = true
		}
	}
	for _, n := range names {
		if carried[n] && !(s.StrictEnv && n == "PATH") {
			kept = append(kept, n)
		} else {
			dropped = append(dropped, n)
		}
	}
	return kept, dropped, nil
}

// printEnvFrom writes the kept and dropped names, one list per line.
func printEnvFrom(w io.Writer, kept, dropped []string) {
	_, _ = fmt.Fprintln(w, "env-from:")
	_, _ = fmt.Fprintf(w, "  kept: %s\n", namesText(kept))
	_, _ = fmt.Fprintf(w, "  dropped: %s\n", namesText(dropped))
}

func namesText(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, " ")
}
