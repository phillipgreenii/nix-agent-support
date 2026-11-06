package config

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
)

// Problem is one configuration defect, naming the table and the entry id.
type Problem struct {
	Table   string
	ID      string
	Message string
}

// String renders the doctor line: <table> "<id>": <message>.
func (p Problem) String() string {
	return fmt.Sprintf("%s %q: %s", p.Table, p.ID, p.Message)
}

// Efforts are the effort levels ccpool accepts.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Validate checks the effective configuration and returns every problem in a
// stable order. Built-in entries are validated like any other, so a missing
// built-in provider command is a problem. It reads no router configuration.
func (e *Effective) Validate() []Problem {
	probs := append([]Problem(nil), e.mergeProblems...)
	add := func(table, id, format string, args ...any) {
		probs = append(probs, Problem{Table: table, ID: id, Message: fmt.Sprintf(format, args...)})
	}

	for _, p := range e.Profiles {
		if p.Model == "" {
			add("profile", p.Name, "no model")
		}
		if p.Effort != "" && !slices.Contains(Efforts, p.Effort) {
			add("profile", p.Name, "effort %q is not one of low, medium, high, xhigh, max", p.Effort)
		}
	}

	for _, s := range e.Scopes {
		if len(s.Prefixes) == 0 {
			add("scope", s.Name, "no prefix")
		}
	}

	for _, p := range e.Providers {
		switch {
		case len(p.Command) == 0 || p.Command[0] == "":
			add("provider", p.Name, "command is missing")
		default:
			if _, err := exec.LookPath(p.Command[0]); err != nil {
				add("provider", p.Name, "command %q not found: not an executable file and not on PATH", p.Command[0])
			}
		}
		if p.ArgsStyle != "" && p.ArgsStyle != "cwd-prefix" && p.ArgsStyle != "inventory" {
			add("provider", p.Name, "args_style %q is not cwd-prefix or inventory", p.ArgsStyle)
		}
	}

	for _, l := range e.Lenses {
		if _, ok := e.Profile(l.Profile); !ok {
			if l.Profile == "" {
				add("lens", l.ID, "profile is required")
			} else {
				add("lens", l.ID, "unknown profile %q", l.Profile)
			}
		}
		if len(l.Providers) == 0 {
			add("lens", l.ID, "no providers")
		}
		for _, name := range l.Providers {
			if _, ok := e.Provider(name); !ok {
				add("lens", l.ID, "unknown provider %q", name)
			}
		}
		if len(l.Kinds) == 0 {
			add("lens", l.ID, "empty kind list")
		}
		if l.Prompt == "" {
			add("lens", l.ID, "prompt is required")
		} else if !e.promptExists(l.Prompt) {
			add("lens", l.ID, "prompt file %q does not exist", l.Prompt)
		}
		if l.BasePrompt != "" && !e.promptExists(l.BasePrompt) {
			add("lens", l.ID, "base_prompt file %q does not exist", l.BasePrompt)
		}
	}
	return probs
}

// promptExists looks for a prompt path first under the config directory
// (absolute paths are taken as given), then in the embedded fs.FS.
func (e *Effective) promptExists(p string) bool {
	disk := p
	if !filepath.IsAbs(p) {
		disk = filepath.Join(e.Env.Dir(), p)
	}
	if st, err := os.Stat(disk); err == nil && !st.IsDir() {
		return true
	}
	if e.Builtins == nil || filepath.IsAbs(p) {
		return false
	}
	clean := path.Clean(filepath.ToSlash(p))
	if !fs.ValidPath(clean) {
		return false
	}
	st, err := fs.Stat(e.Builtins, clean)
	return err == nil && !st.IsDir()
}

// Warnings returns the non-fatal notes about the effective configuration:
// currently only that no scope is configured (scopes have no built-in).
func (e *Effective) Warnings() []string {
	if len(e.Scopes) == 0 {
		return []string{"warning: no scope is configured; add a [[scope]] entry to config.toml before running a review"}
	}
	return nil
}
