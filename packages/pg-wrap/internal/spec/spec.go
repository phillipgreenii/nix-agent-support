// Package spec reads a wrap spec: pg-wrap's own TOML configuration file.
//
// A spec has a fixed set of keys: schema_version (an integer, required), wrap,
// strict_env, keep and exec. An unknown key, a wrong type, a missing
// schema_version and a version newer than this pg-wrap supports are each a
// decode error; the decoder never guesses, ignores or defaults past one.
package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/phillipgreenii/pg-wrap/internal/fail"
	"github.com/phillipgreenii/pg-wrap/internal/wrap"
)

// MaxSchemaVersion is the highest schema_version this pg-wrap supports.
const MaxSchemaVersion = 1

// Spec is a decoded wrap spec.
type Spec struct {
	SchemaVersion int
	// Wrap is the ordered entries, outermost first.
	Wrap []wrap.Entry
	// StrictEnv starts the layers from the minimal environment instead of the
	// inherited one.
	StrictEnv bool
	// Keep names variables the minimal environment also keeps, by exact name.
	Keep []string
	// Exec is an optional argv prefix (absolute first element) the payload is
	// preceded by, in both run and pg-wrap-exec.
	Exec []string
}

// raw mirrors the spec's keys. Wrap stays untyped: wrap.Decode owns its shape.
type raw struct {
	SchemaVersion *int64   `toml:"schema_version"`
	Wrap          any      `toml:"wrap"`
	StrictEnv     bool     `toml:"strict_env"`
	Keep          []string `toml:"keep"`
	Exec          []string `toml:"exec"`
}

// Load reads and decodes the spec at path. Every error is a *fail.Error at the
// spec stage.
func Load(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fail.New(fail.StageSpec, "cannot read %s: %v", path, err)
	}
	return Decode(path, data)
}

// Decode decodes spec text. path is used only in messages.
func Decode(path string, data []byte) (*Spec, error) {
	// The version is read on its own first, so a spec from a newer pg-wrap is
	// refused by version and not by whichever of its new keys is met first.
	var head struct {
		SchemaVersion *int64 `toml:"schema_version"`
	}
	if _, err := toml.Decode(string(data), &head); err != nil {
		return nil, fail.New(fail.StageSpec, "%s: %s", path, oneLine(err))
	}
	if head.SchemaVersion == nil {
		return nil, fail.New(fail.StageSpec, "%s: schema_version is required", path)
	}
	if v := *head.SchemaVersion; v > MaxSchemaVersion {
		return nil, fail.New(fail.StageSpec, "%s has schema_version %d, this pg-wrap supports <= %d.", path, v, MaxSchemaVersion)
	} else if v < 1 {
		return nil, fail.New(fail.StageSpec, "%s: schema_version %d is not supported (the lowest is 1)", path, v)
	}

	var r raw
	md, err := toml.Decode(string(data), &r)
	if err != nil {
		return nil, fail.New(fail.StageSpec, "%s: %s", path, oneLine(err))
	}
	// wrap is checked before the unknown-key scan: a table inside wrap shows up
	// there as an unknown key, and "entry must be a string or a list" says more.
	entries, err := wrap.Decode(r.Wrap)
	if err != nil {
		return nil, fail.New(fail.StageSpec, "%s: %v", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		names := make([]string, len(und))
		for i, k := range und {
			names[i] = k.String()
		}
		sort.Strings(names)
		return nil, fail.New(fail.StageSpec, "%s: unknown key %s", path, strings.Join(names, ", "))
	}
	return &Spec{
		SchemaVersion: int(*r.SchemaVersion),
		Wrap:          entries,
		StrictEnv:     r.StrictEnv,
		Keep:          r.Keep,
		Exec:          r.Exec,
	}, nil
}

func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

// Payload returns the exec prefix followed by args: the argv the innermost layer
// receives.
func (s *Spec) Payload(args []string) []string {
	out := make([]string, 0, len(s.Exec)+len(args))
	out = append(out, s.Exec...)
	return append(out, args...)
}

// Ref is one path a spec references: the program a layer or the exec prefix
// names.
type Ref struct {
	// Stage is the failure stage that names it: "layer <i>" or "spec" (exec).
	Stage string
	// What describes it for a message, for example "wrap[1]" or "exec".
	What string
	// Path is the first word or first element.
	Path string
	// Err is non-nil when the entry itself is malformed or the path is not
	// absolute.
	Err error
}

// Refs lists every path the spec references, in layer order and then exec,
// with the absolute-path rule (WC-9, INV-SPEC-6) already applied to each.
func (s *Spec) Refs() []Ref {
	var out []Ref
	for i, e := range s.Wrap {
		out = append(out, Ref{
			Stage: fail.Layer(i), What: fmt.Sprintf("wrap[%d]", i),
			Path: e.FirstWord(), Err: wrap.CheckEntry(e),
		})
	}
	if len(s.Exec) > 0 {
		var err error
		if !filepath.IsAbs(s.Exec[0]) {
			err = fmt.Errorf("first element %q of exec must be an absolute path", s.Exec[0])
		}
		out = append(out, Ref{Stage: fail.StageSpec, What: "exec", Path: s.Exec[0], Err: err})
	}
	return out
}

// PathProblem reports why path is not an executable regular file, or "". A
// symlink is followed, so a broken symlink is a problem.
func PathProblem(path string) string {
	fi, err := os.Stat(path)
	switch {
	case err != nil:
		if li, lerr := os.Lstat(path); lerr == nil && li.Mode()&os.ModeSymlink != 0 {
			return fmt.Sprintf("%s: is a broken symlink", path)
		}
		return fmt.Sprintf("%s: %v", path, unwrapPathError(err))
	case fi.IsDir():
		return fmt.Sprintf("%s: is a directory, not an executable file", path)
	case !fi.Mode().IsRegular():
		return fmt.Sprintf("%s: is not a regular file", path)
	case fi.Mode().Perm()&0o111 == 0:
		return fmt.Sprintf("%s: is not executable", path)
	}
	return ""
}

func unwrapPathError(err error) error {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err
	}
	return err
}
