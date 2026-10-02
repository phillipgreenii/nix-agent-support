// Package display renders what pg-rescue shows the operator when a handler
// ran: a header, one section per handler tried, and a footer. Nothing is shown
// on the happy path.
//
// Output rules. The display is written to stderr only. Tool text appears only
// on delimiter lines, "== ... ==" for sections and "-- ... --" for
// sub-sections. Text a handler, the command or verify produced is printed
// flush-left on lines of its own, with none of the tool's text before or after
// it on the same line, so it can be copied. A blank line comes before every
// handler section. There is no colour.
//
// Render is pure: it builds the text from the in-memory result, so the same
// renderer serves stderr (at the caller's verbosity) and display.log (always
// at -vv).
package display

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/config"
	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

// Tail limits of the handler stderr and verify output shown at -v and -vv: the
// same caps as the report's output_tail.
const (
	tailLines = runner.TailLines
	tailBytes = runner.TailBytes
)

// Input is everything the renderer needs besides the verbosity.
type Input struct {
	Result *runner.Result
	// Handlers are the configured instances, for each attempt's argv, timeout
	// and description. A name that is missing renders without them.
	Handlers map[string]*config.Handler
	// RunDir is the run directory shown at -v.
	RunDir string
	// Home is abbreviated to "~" in the run directory line.
	Home string
	// Redact is applied to every piece of text shown (the report's own copies
	// are already redacted). nil means no redaction.
	Redact func(string) string
	// ReadFile reads a per-attempt stderr or verify file; nil means os.ReadFile.
	ReadFile func(string) ([]byte, error)
}

// Shown reports whether a run has anything to display: a handler ran.
func Shown(res *runner.Result) bool {
	return res != nil && res.Report != nil && len(res.Report.Attempts) > 0
}

// Lead is what must be written to stderr before the display so the header is
// never glued to the command's own output (R9): it ends the line the command
// left open (if any) and leaves one blank line. It is "" when the command
// printed nothing. display.log has no lead; it holds the display alone.
func Lead(res *runner.Result) string {
	switch {
	case res == nil || !res.Passthrough:
		return ""
	case res.MidLine:
		return "\n\n"
	}
	return "\n"
}

// Render returns the display for in at level, or "" when there is nothing to
// show (no handler ran) or level is Quiet.
func Render(in Input, level cli.Verbosity) string {
	if level == cli.Quiet || !Shown(in.Result) {
		return ""
	}
	r := &renderer{in: in, level: level}
	if r.in.Redact == nil {
		r.in.Redact = func(s string) string { return s }
	}
	if r.in.ReadFile == nil {
		r.in.ReadFile = os.ReadFile
	}
	return r.render()
}

type renderer struct {
	in    Input
	level cli.Verbosity
	b     strings.Builder
}

func (r *renderer) line(format string, a ...any) {
	fmt.Fprintf(&r.b, format+"\n", a...)
}

// prep prepares handler-produced text for printing: sanitized FIRST (so a
// control character cannot split a secret and slip past the patterns), then
// redacted, then laid out flush-left on lines of its own.
func (r *renderer) prep(s string) string {
	return handlerText(r.in.Redact(Sanitize(s)))
}

func (r *renderer) render() string {
	res := r.in.Result
	rep := res.Report

	r.line("== pg-rescue: %s · %s ==", r.subject(), r.selector())

	n := len(rep.Handlers)
	for i := range rep.Attempts {
		r.attempt(&rep.Attempts[i], n)
	}
	if r.level >= cli.Verbose {
		for pos := len(rep.Attempts) + 1; pos <= n; pos++ {
			r.b.WriteString("\n")
			r.line("== [%d/%d] %s: skipped (chain stopped) ==", pos, n, inline(rep.Handlers[pos-1]))
		}
	}

	r.b.WriteString("\n")
	r.line("== %s ==", r.footer())
	if r.level >= cli.Verbose && r.in.RunDir != "" {
		r.line("== run dir: %s ==", inline(r.abbreviate(r.in.RunDir)))
	}
	return r.b.String()
}

// subject is "`cmd` exited N", or "stdin input" in --stdin mode.
func (r *renderer) subject() string {
	rep := r.in.Result.Report
	if rep.Command == nil {
		return "stdin input"
	}
	return fmt.Sprintf("`%s` exited %d", inline(r.in.Redact(report.QuoteArgv(rep.Command.Argv))), rep.Command.Exit)
}

// selector is "chain NAME", or "handlers a,b,c" under --handlers.
func (r *renderer) selector() string {
	rep := r.in.Result.Report
	if rep.Chain != nil {
		return "chain " + inline(*rep.Chain)
	}
	return "handlers " + inline(strings.Join(rep.Handlers, ","))
}

func (r *renderer) footer() string {
	res := r.in.Result
	run := "run " + inline(res.Report.RunID)
	switch res.Kind {
	case runner.KindResolved:
		return fmt.Sprintf("resolved by %s · %s", inline(res.ResolvedBy), run)
	case runner.KindDeferred:
		return fmt.Sprintf("deferred by %s · exit %d · working copy left as-is · %s", inline(res.DeferredBy), res.ExitCode, run)
	case runner.KindInterrupted:
		in := res.Interrupted
		if in != nil && in.Handler != "" {
			return fmt.Sprintf("interrupted during %s · working copy may be mid-change · exit %d · %s", inline(in.Handler), res.ExitCode, run)
		}
		return fmt.Sprintf("interrupted · exit %d · %s", res.ExitCode, run)
	}
	return fmt.Sprintf("unhandled · exit %d · %s", res.ExitCode, run)
}

func (r *renderer) abbreviate(path string) string {
	if h := strings.TrimRight(r.in.Home, "/"); h != "" && strings.HasPrefix(path, h+"/") {
		return "~" + path[len(h):]
	}
	return path
}

// attempt renders one handler's section.
func (r *renderer) attempt(a *report.Attempt, n int) {
	h := r.in.Handlers[a.Handler]
	r.b.WriteString("\n")
	r.line("== [%d/%d] %s: %s%s ==", a.Position, n, inline(a.Handler), a.Outcome, r.parenthetical(a, h))
	r.b.WriteString(r.prep(firstLine(a.Reported.Summary)))

	if r.level >= cli.VeryVerbose && h != nil && len(h.Command) > 0 {
		r.line("-- argv --")
		r.b.WriteString(r.prep(report.QuoteArgv(h.Command)))
	}
	if r.level >= cli.Verbose {
		if d := r.prep(a.Reported.Details); d != "" {
			r.line("-- details --")
			r.b.WriteString(d)
		}
	}
	if r.level >= cli.VeryVerbose {
		if m := compactJSON(a.Reported.Meta); m != "" {
			r.line("-- meta --")
			r.b.WriteString(r.prep(m))
		}
		if s := r.fileTail(a.StderrFile); s != "" {
			r.line("-- stderr --")
			r.b.WriteString(s)
		}
	}
	r.verify(a)
}

// verify renders the verify sub-section: at the default level only a passed
// verify gets a delimiter (a failed one is already in the attempt's header);
// -v adds failures and the verify output.
func (r *renderer) verify(a *report.Attempt) {
	if a.VerifyMS == nil && a.VerifyOutputFile == "" {
		return
	}
	passed := a.Reason == "verify passed"
	if r.level < cli.Verbose && !passed {
		return
	}
	state, detail := "passed", "exit 0"
	if !passed {
		state, detail = "failed", verifyDetail(a.Reason)
	}
	if r.level >= cli.VeryVerbose && a.VerifyMS != nil {
		detail += ", " + Duration(time.Duration(*a.VerifyMS)*time.Millisecond)
	}
	r.line("-- verify: %s (%s) --", state, inline(detail))
	if r.level >= cli.Verbose {
		r.b.WriteString(r.fileTail(a.VerifyOutputFile))
	}
}

// fileTail is the last lines of a per-attempt file, as printable handler text.
func (r *renderer) fileTail(path string) string {
	if path == "" {
		return ""
	}
	data, err := r.in.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	s := runner.LimitTail(strings.ToValidUTF8(string(data), "�"), tailLines, tailBytes, true)
	return r.prep(s)
}

// parenthetical is the text after the outcome on an attempt's delimiter line.
// At the default level it is the reason (omitted for a resolved or deferred
// attempt, whose reason is only the exit code or "verify passed"). At -vv it
// lists the reason, exit code, duration, timeout and description.
func (r *renderer) parenthetical(a *report.Attempt, h *config.Handler) string {
	reason := shortReason(a.Reason)
	trivial := reason == "" || reason == fmt.Sprintf("exit %d", a.Exit) || reason == "verify passed"
	var parts []string
	if r.level < cli.VeryVerbose {
		if !trivial || a.Outcome == contract.Declined || a.Outcome == contract.Failed {
			if reason != "" {
				parts = append(parts, reason)
			}
		}
	} else {
		if !trivial {
			parts = append(parts, reason)
		}
		if a.Exit >= 0 {
			parts = append(parts, fmt.Sprintf("exit %d", a.Exit))
		}
		parts = append(parts, Duration(time.Duration(a.DurationMS)*time.Millisecond))
		if h != nil {
			parts = append(parts, "timeout "+Timeout(h.Timeout))
			if h.Description != "" {
				parts = append(parts, h.Description)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + inline(r.in.Redact(strings.Join(parts, ", "))) + ")"
}

var reVerifyFailed = regexp.MustCompile(`^failed \((.*)\)$`)

const verifyPrefix = "resolved → verify "

// shortReason words a recorded reason for a delimiter line. The recorded
// "resolved → verify failed (exit 128)" reads "verify failed: exit 128" here,
// since the outcome next to it already says what the handler claimed.
func shortReason(reason string) string {
	rest, ok := strings.CutPrefix(reason, verifyPrefix)
	if !ok {
		return reason
	}
	if m := reVerifyFailed.FindStringSubmatch(rest); m != nil {
		return "verify failed: " + m[1]
	}
	return "verify " + rest
}

// verifyDetail is what a failed verify's delimiter shows in parentheses.
func verifyDetail(reason string) string {
	rest := strings.TrimPrefix(reason, verifyPrefix)
	if m := reVerifyFailed.FindStringSubmatch(rest); m != nil {
		return m[1]
	}
	return rest
}

// compactJSON is a handler's meta on one line, in the handler's own key order.
func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// Duration formats a measured duration for the display: "310ms", "1.4s",
// "3m12s", "1h2m3s".
func Duration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		s := int(d.Round(time.Second) / time.Second)
		return fmt.Sprintf("%dm%ds", s/60, s%60)
	}
	s := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%dh%dm%ds", s/3600, s%3600/60, s%60)
}

// Timeout formats a configured timeout the way a config author wrote it:
// "10m", "90s", "1h".
func Timeout(d time.Duration) string {
	switch {
	case d <= 0:
		return "none"
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}
