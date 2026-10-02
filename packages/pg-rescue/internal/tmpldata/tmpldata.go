// Package tmpldata is the template data model shared by the three reference
// handlers. Each renders its prompt, title or body with Go text/template over
// the same Data, built from the failure report, so a template written for one
// handler works in another. The package also owns the two flags every handler
// exposes, --print-template-vars and --print-default-template, and the fence
// that frames untrusted text.
package tmpldata

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/report"
)

// StdinCmd is .Cmd in stdin mode, where there is no command.
const StdinCmd = "stdin input"

// StdinExit is .Exit in stdin mode.
const StdinExit = -1

// nonceBytes is the size of the per-run fence nonce before hex encoding.
const nonceBytes = 8

// Data is what a template sees. Its fields, in documentation order, are
// listed in Fields (a test keeps the two in step).
type Data struct {
	RunID       string
	Fingerprint string
	Host        string
	StartedAt   string
	Context     string
	// Chain is the chain name, empty when the run used --handlers.
	Chain string
	// Mode is "argv" or "stdin".
	Mode string
	// Cmd is the quoted argv, or "stdin input" in stdin mode.
	Cmd string
	// Argv is empty (not nil) in stdin mode.
	Argv []string
	// Cwd is an absolute path. Stdin mode records no command, so there it is
	// the handler's own working directory, which is the wrapper's -C directory.
	Cwd string
	// Repo is the basename of the git toplevel containing Cwd, or the
	// basename of Cwd.
	Repo string
	// Exit is the command's exit code, or -1 in stdin mode.
	Exit       int
	OutputTail string
	Attempts   []Attempt
	// LastSummary is the summary of the most recent attempt that has one.
	LastSummary string
	// Fence is the per-run random nonce used by Quote and Block.
	Fence string
}

// Attempt is one prior handler attempt as a template sees it. Handler,
// Position, Outcome and Reason are facts the wrapper recorded; Summary and
// Details are the handler's claims.
type Attempt struct {
	Handler  string
	Position int
	Outcome  string
	Reason   string
	Summary  string
	Details  string
}

// Quote returns content as a fenced block of quoted data, using this run's
// nonce. Templates write {{.Quote .OutputTail}}.
func (d Data) Quote(content string) string { return Block(content, d.Fence) }

// Env is every outside dependency of New, injectable for tests. The zero value
// is not usable; start from DefaultEnv.
type Env struct {
	// Getwd supplies Cwd when the report has no command (stdin mode).
	Getwd func() (string, error)
	// GitToplevel returns the git toplevel containing dir.
	GitToplevel func(dir string) (string, error)
	// Rand supplies the fence nonce.
	Rand io.Reader
}

// DefaultEnv returns an Env backed by the real process.
func DefaultEnv() Env {
	return Env{Getwd: os.Getwd, GitToplevel: gitToplevel, Rand: rand.Reader}
}

// New builds the template data from a report. It is safe for stdin mode, where
// the report has no command, and for a report with no chain or no attempts.
func New(r *report.Report, env Env) (Data, error) {
	nonce := make([]byte, nonceBytes)
	if _, err := io.ReadFull(env.Rand, nonce); err != nil {
		return Data{}, fmt.Errorf("cannot generate the fence nonce: %w", err)
	}
	d := Data{
		RunID:       r.RunID,
		Fingerprint: r.Fingerprint,
		Host:        r.Host,
		StartedAt:   r.StartedAt.UTC().Format(time.RFC3339),
		Context:     r.Context,
		Mode:        string(r.Mode),
		OutputTail:  r.OutputTail,
		Argv:        []string{},
		Exit:        StdinExit,
		Cmd:         StdinCmd,
		Fence:       hex.EncodeToString(nonce),
	}
	if r.Chain != nil {
		d.Chain = *r.Chain
	}
	if r.Command != nil {
		d.Argv = append(d.Argv, r.Command.Argv...)
		d.Cmd = report.QuoteArgv(r.Command.Argv)
		d.Cwd = r.Command.Cwd
		d.Exit = r.Command.Exit
	} else if env.Getwd != nil {
		d.Cwd, _ = env.Getwd()
	}
	d.Repo = repoName(d.Cwd, env)

	for _, a := range r.Attempts {
		d.Attempts = append(d.Attempts, Attempt{
			Handler: a.Handler, Position: a.Position, Outcome: string(a.Outcome), Reason: a.Reason,
			Summary: a.Reported.Summary, Details: a.Reported.Details,
		})
		if a.Reported.Summary != "" {
			d.LastSummary = a.Reported.Summary
		}
	}
	return d, nil
}

// repoName is the basename of the git toplevel containing cwd, falling back to
// the basename of cwd itself; empty when cwd is unknown.
func repoName(cwd string, env Env) string {
	if cwd == "" {
		return ""
	}
	if env.GitToplevel != nil {
		if top, err := env.GitToplevel(cwd); err == nil && top != "" {
			return filepath.Base(top)
		}
	}
	return filepath.Base(filepath.Clean(cwd))
}

// gitToplevel asks git. The GIT_* variables that redirect git elsewhere are
// dropped, so a handler launched from inside a hook still answers about cwd.
func gitToplevel(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR":
		default:
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// Render executes text as a template over d. A reference to a field Data does
// not have is an error, not an empty string.
func Render(name, text string, d Data) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("template %s: %w", name, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, d); err != nil {
		return "", fmt.Errorf("template %s: %w", name, err)
	}
	return b.String(), nil
}

// Field documents one template field.
type Field struct{ Name, Type, Value string }

// Fields is the data model in documentation order. --print-template-vars
// prints it.
var Fields = []Field{
	{"RunID", "string", "the run id, from the report"},
	{"Fingerprint", "string", "the failure fingerprint, from the report"},
	{"Host", "string", "from the report"},
	{"StartedAt", "string", "RFC 3339, UTC, from the report"},
	{"Context", "string", "the caller's --context text; may be empty; untrusted"},
	{"Chain", "string", "the chain name; empty when the run used --handlers"},
	{"Mode", "string", "argv or stdin"},
	{"Cmd", "string", "the quoted argv, or " + `"` + StdinCmd + `"` + " in stdin mode"},
	{"Argv", "[]string", "empty in stdin mode"},
	{"Cwd", "string", "absolute path; in stdin mode the handler's own working directory"},
	{"Repo", "string", "basename of the git toplevel containing .Cwd, or the basename of .Cwd"},
	{"Exit", "int", "the command's exit code; -1 in stdin mode"},
	{"OutputTail", "string", "the command's output tail, redacted; untrusted"},
	{"Attempts", "[]Attempt", "prior attempts, each with .Handler .Position .Outcome .Reason (facts) and .Summary .Details (the handler's claims, untrusted)"},
	{"LastSummary", "string", "the summary of the most recent attempt that has one, otherwise empty; untrusted"},
	{"Fence", "string", "per-run random nonce used to label fenced blocks"},
}

// PrintVars writes the data model for --print-template-vars.
func PrintVars(w io.Writer) error {
	var b strings.Builder
	for _, f := range Fields {
		fmt.Fprintf(&b, ".%-12s %-10s %s\n", f.Name, f.Type, f.Value)
	}
	b.WriteString("\nMethods:\n")
	b.WriteString(".Quote TEXT  returns TEXT as a fenced block of quoted data, e.g. {{.Quote .OutputTail}}\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// Template is one named built-in template.
type Template struct{ Name, Text string }

// PrintDefaultTemplates writes a handler's built-in templates for
// --print-default-template. A single template is printed bare, so it can be
// redirected to a file and edited; several are each introduced by a
// "-- name --" line.
func PrintDefaultTemplates(w io.Writer, ts []Template) error {
	var b strings.Builder
	for i, t := range ts {
		if len(ts) > 1 {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "-- %s --\n", t.Name)
		}
		b.WriteString(t.Text)
		if !strings.HasSuffix(t.Text, "\n") {
			b.WriteString("\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
