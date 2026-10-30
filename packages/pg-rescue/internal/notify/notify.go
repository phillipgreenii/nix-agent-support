// Package notify is pg-rescue-notify: the reference handler that posts a
// macOS notification about a failure and handles nothing. It always declines
// (exit 2) so the chain continues, and exits 1 if the notification cannot be
// posted.
//
// The notification is posted over the pg-osx-notifier daemon's Unix socket
// as a JSON request; the title, body and sound name come from templates over
// untrusted text (command output, an earlier handler's summary) and travel as
// JSON string values, never as script text. History: this handler used to run
// a fixed AppleScript through osascript; when the daemon is unreachable it now
// logs and drops, with no fallback.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/spf13/pflag"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/notifyclient"
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

// Field caps of the daemon's contract, in bytes. The handler cuts before
// sending, because the daemon rejects over-cap input.
const (
	maxTitleBytes = 256
	maxBodyBytes  = 2048
	maxSoundBytes = 64
)

// soundName is the form of a sound name the daemon accepts.
var soundName = regexp.MustCompile(`^[A-Za-z0-9 _-]{1,64}$`)

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
	// cancels the in-flight post. It returns the stop function.
	Watch func(cleanup func()) (stop func())
	// Tmpl feeds tmpldata.New.
	Tmpl tmpldata.Env
	// Getenv reads the handler's environment (PG_RESCUE_REPORT); os.Getenv
	// when nil. It also feeds the socket discovery rule.
	Getenv func(string) string
	// PostTimeout bounds one post; the contract's 7 s when zero.
	PostTimeout time.Duration
}

// DefaultRuntime is backed by the real process.
func DefaultRuntime() Runtime {
	return Runtime{Watch: orphan.Watch, Tmpl: tmpldata.DefaultEnv(), Getenv: os.Getenv}
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

	getenv := rt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	data, err := loadData(rt, getenv("PG_RESCUE_REPORT"))
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

	title, body = Sanitize(title, maxTitleBytes), Sanitize(body, maxBodyBytes)
	sound := Sanitize(o.sound, 1<<20) // never cut: an over-cap sound is refused below
	if title == "" {
		return failf(stderr, "the title is empty after trimming; nothing posted")
	}
	if sound != "" && !soundName.MatchString(sound) {
		return failf(stderr, "--sound %q is not 1 to 64 bytes of [A-Za-z0-9 _-]; nothing posted", sound)
	}
	if sound == "" {
		sound = "none"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := func() {}
	if rt.Watch != nil {
		stop = rt.Watch(cancel)
	}
	defer stop()
	client := notifyclient.Client{Getenv: getenv, Timeout: rt.PostTimeout}
	if err := client.Post(ctx, title, body, sound); err != nil {
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
