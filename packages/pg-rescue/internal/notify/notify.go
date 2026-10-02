// Package notify is pg-rescue-notify: the reference handler that posts a
// macOS notification about a failure and handles nothing. It always declines
// (exit 2) so the chain continues, and exits 1 if the notification cannot be
// posted.
//
// Security: the title, body and sound name come from templates over untrusted
// text (command output, an earlier handler's summary). They are handed to a
// FIXED AppleScript as argv items, never interpolated into script text, so
// nothing they contain can be run as AppleScript or as `do shell script`.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/spf13/pflag"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/orphan"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/tmpldata"
)

// Handler exit codes. Declined is the contract's code for "I did nothing to
// the failure"; Failed is the generic failure (also what a usage error uses:
// a handler MUST NOT leak 2 from a usage error, which would read as declined).
const (
	ExitDeclined = contract.ExitDeclined
	ExitFailed   = 1
)

// Default templates (design section 8.4).
const (
	DefaultTitleTemplate = "pg-rescue: {{.Cmd}} failed"
	DefaultBodyTemplate  = "{{.Repo}}: {{.LastSummary}}"
)

// Script is the AppleScript, line by line, passed to osascript with -e. It is
// a constant: no value is ever formatted into it. Item 1 of argv is the title,
// item 2 the body and item 3 the sound name (empty for none).
var Script = []string{
	"on run argv",
	"set theTitle to item 1 of argv",
	"set theBody to item 2 of argv",
	"set theSound to item 3 of argv",
	"if theSound is \"\" then",
	"display notification theBody with title theTitle",
	"else",
	"display notification theBody with title theTitle sound name theSound",
	"end if",
	"end run",
}

// postTimeout bounds one osascript run.
const postTimeout = 30 * time.Second

const usage = `usage: pg-rescue-notify [--title-template TEXT | --title-template-file F]
                        [--body-template TEXT | --body-template-file F] [--sound NAME]
       pg-rescue-notify --print-template-vars
       pg-rescue-notify --print-default-template

Posts a macOS notification about the failure in $PG_RESCUE_REPORT and always
declines (exit 2), so the chain continues. Exits 1 if posting fails.
`

// Runtime holds the outside dependencies tests inject. DefaultRuntime is the
// real process.
type Runtime struct {
	// Watch starts the orphan watch (orphan.Watch in production). cleanup
	// kills the in-flight osascript. It returns the stop function.
	Watch func(cleanup func()) (stop func())
	// Tmpl feeds tmpldata.New.
	Tmpl tmpldata.Env
}

// DefaultRuntime is backed by the real process.
func DefaultRuntime() Runtime {
	return Runtime{Watch: orphan.Watch, Tmpl: tmpldata.DefaultEnv()}
}

type options struct {
	title, titleFile, body, bodyFile, sound string
	hasTitle, hasTitleFile                  bool
	hasBody, hasBodyFile                    bool
	printVars, printDefault, help           bool
}

func parse(args []string) (*options, error) {
	var o options
	fs := pflag.NewFlagSet("pg-rescue-notify", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.title, "title-template", "", "")
	fs.StringVar(&o.titleFile, "title-template-file", "", "")
	fs.StringVar(&o.body, "body-template", "", "")
	fs.StringVar(&o.bodyFile, "body-template-file", "", "")
	fs.StringVar(&o.sound, "sound", "", "")
	fs.BoolVar(&o.printVars, "print-template-vars", false, "")
	fs.BoolVar(&o.printDefault, "print-default-template", false, "")
	fs.BoolVarP(&o.help, "help", "h", false, "")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	o.hasTitle, o.hasTitleFile = fs.Changed("title-template"), fs.Changed("title-template-file")
	o.hasBody, o.hasBodyFile = fs.Changed("body-template"), fs.Changed("body-template-file")
	if o.hasTitle && o.hasTitleFile {
		return nil, errors.New("--title-template and --title-template-file are mutually exclusive")
	}
	if o.hasBody && o.hasBodyFile {
		return nil, errors.New("--body-template and --body-template-file are mutually exclusive")
	}
	return &o, nil
}

// Run is the handler. It returns the process exit code.
func Run(rt Runtime, args []string, stdout, stderr io.Writer) int {
	o, err := parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "pg-rescue-notify: %v\n%s", err, usage)
		return ExitFailed
	}
	switch {
	case o.help:
		fmt.Fprint(stdout, usage)
		return 0
	case o.printVars:
		return printOrFail(tmpldata.PrintVars(stdout), stderr)
	case o.printDefault:
		return printOrFail(tmpldata.PrintDefaultTemplates(stdout, []tmpldata.Template{
			{Name: "title", Text: DefaultTitleTemplate},
			{Name: "body", Text: DefaultBodyTemplate},
		}), stderr)
	}

	titleTmpl, err := templateText(o.title, o.hasTitle, o.titleFile, o.hasTitleFile, DefaultTitleTemplate)
	if err != nil {
		return failf(stderr, "--title-template-file: %v", err)
	}
	bodyTmpl, err := templateText(o.body, o.hasBody, o.bodyFile, o.hasBodyFile, DefaultBodyTemplate)
	if err != nil {
		return failf(stderr, "--body-template-file: %v", err)
	}

	data, err := loadData(rt, os.Getenv("PG_RESCUE_REPORT"))
	if err != nil {
		return failf(stderr, "%v", err)
	}
	title, err := tmpldata.Render("title", titleTmpl, data)
	if err != nil {
		return failf(stderr, "%v", err)
	}
	body, err := tmpldata.Render("body", bodyTmpl, data)
	if err != nil {
		return failf(stderr, "%v", err)
	}

	p := &poster{}
	stop := func() {}
	if rt.Watch != nil {
		stop = rt.Watch(p.kill)
	}
	defer stop()
	if err := p.post(Sanitize(title), Sanitize(body), Sanitize(o.sound)); err != nil {
		return failf(stderr, "cannot post the notification: %v", err)
	}

	out, err := contract.Result{Outcome: contract.Declined, Summary: "posted a notification"}.Render()
	if err != nil {
		return failf(stderr, "%v", err)
	}
	if _, err := stdout.Write(out); err != nil {
		return failf(stderr, "cannot write the result: %v", err)
	}
	return ExitDeclined
}

func printOrFail(err error, stderr io.Writer) int {
	if err != nil {
		return failf(stderr, "%v", err)
	}
	return 0
}

func failf(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "pg-rescue-notify: "+format+"\n", a...)
	return ExitFailed
}

// templateText picks the inline text, the file's content, or the default.
func templateText(inline string, hasInline bool, file string, hasFile bool, def string) (string, error) {
	switch {
	case hasInline:
		return inline, nil
	case hasFile:
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return def, nil
}

// loadData reads the failure report the wrapper wrote and builds the template
// data. A report with a schema_version this build does not know is refused
// (exit 1), as the report contract asks of handlers.
func loadData(rt Runtime, path string) (tmpldata.Data, error) {
	if path == "" {
		return tmpldata.Data{}, errors.New("PG_RESCUE_REPORT is not set; this handler runs under pg-rescue")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return tmpldata.Data{}, fmt.Errorf("cannot read the report: %w", err)
	}
	var r report.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return tmpldata.Data{}, fmt.Errorf("cannot parse the report %s: %w", path, err)
	}
	if r.SchemaVersion != report.SchemaVersion {
		return tmpldata.Data{}, fmt.Errorf("unknown report schema_version %d (this handler knows %d)", r.SchemaVersion, report.SchemaVersion)
	}
	return tmpldata.New(&r, rt.Tmpl)
}

// poster runs osascript and lets the orphan watch kill it.
type poster struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func (p *poster) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// post runs the fixed script with the three values as argv. The `--` ends
// osascript's own option parsing, so a title that starts with "-" is still
// data. The values are already sanitized by the caller.
func (p *poster) post(title, body, sound string) error {
	path, err := exec.LookPath("osascript")
	if err != nil {
		return fmt.Errorf("osascript not found on PATH (macOS only): %w", err)
	}
	args := make([]string, 0, 2*len(Script)+4)
	for _, line := range Script {
		args = append(args, "-e", line)
	}
	args = append(args, "--", title, body, sound)

	ctx, cancel := context.WithTimeout(context.Background(), postTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = nil // /dev/null
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out // never our stdout: that carries the result
	cmd.WaitDelay = time.Second

	p.mu.Lock()
	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.cmd = cmd
	p.mu.Unlock()

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("osascript timed out after %s", postTimeout)
		}
		return fmt.Errorf("osascript: %v: %s", err, bytes.TrimSpace(clip(out.Bytes(), 500)))
	}
	return nil
}

func clip(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}
