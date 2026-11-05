package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// longRequest lifts the client's request timeout for the operations that read
// whole files (doctor, remove, extract).
func longRequest(cl *client.Client) { cl.HTTP.Timeout = 30 * time.Minute }

func (a *app) printJSON(v any) {
	b, _ := json.Marshal(v)
	_, _ = fmt.Fprintln(a.out, string(b))
}

// held lists the held Conversations: stable columns, one reason code each, and
// a remedy line. --retry re-runs the check first.
func (a *app) held(args []string) int {
	var c common
	var retry bool
	fs := newFlagSet("held", a.errw)
	c.bind(fs)
	fs.BoolVar(&retry, "retry", false, "re-run the settle check of every held Conversation now")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	if fs.NArg() > 0 {
		return a.emitError(c.jsonOut, wire.ErrUsage, "held takes no arguments")
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	var ans serve.HeldAnswer
	if err := cl.Post(a.ctx, "held", serve.HeldRequest{Retry: retry}, &ans); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	if ans.Held == nil {
		ans.Held = []settle.HeldRow{}
	}
	if c.jsonOut {
		a.printJSON(ans)
		return wire.ExitOK
	}
	if retry {
		_, _ = fmt.Fprintf(a.out, "re-checked %d held Conversation(s); %d cleared\n", ans.Retried, ans.Cleared)
	}
	if len(ans.Held) == 0 {
		_, _ = fmt.Fprintln(a.out, "0 held")
		return wire.ExitOK
	}
	_, _ = fmt.Fprintln(a.out, "SESSION  REASON  SINCE  FILES  REMEDY")
	for _, h := range ans.Held {
		_, _ = fmt.Fprintf(a.out, "%s  %s  %s  %d  %s\n", h.Session, h.Reason, h.Since.Format(time.RFC3339), h.Files, h.Remedy)
		if h.Detail != "" {
			_, _ = fmt.Fprintf(a.out, "    %s\n", h.Detail)
		}
	}
	return wire.ExitOK
}

// where prints a Conversation's stage, what blocks its removal and the command
// that clears it.
func (a *app) where(args []string) int {
	var c common
	fs := newFlagSet("where", a.errw)
	c.bind(fs)
	if err := fs.Parse(reorderValue(args, valuedFlags)); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	if fs.NArg() != 1 {
		return a.emitError(c.jsonOut, wire.ErrUsage, "where takes one argument: a session id")
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	var w settle.WhereInfo
	if err := cl.Post(a.ctx, "where", serve.WhereRequest{Session: fs.Arg(0)}, &w); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	if c.jsonOut {
		a.printJSON(w)
		return wire.ExitOK
	}
	_, _ = fmt.Fprintf(a.out, "%s: stage %s\n", w.Session, w.Stage)
	blocked := "none"
	if len(w.BlockedBy) > 0 {
		blocked = strings.Join(w.BlockedBy, ", ")
	}
	_, _ = fmt.Fprintf(a.out, "blocked_by: %s\n", blocked)
	next := "-"
	if w.NextEligible != nil {
		next = w.NextEligible.UTC().Format(time.RFC3339)
	}
	_, _ = fmt.Fprintf(a.out, "next_eligible: %s\n", next)
	if w.Clear != "" {
		_, _ = fmt.Fprintf(a.out, "clear: %s\n", w.Clear)
	}
	if w.HeldReason != "" {
		_, _ = fmt.Fprintf(a.out, "held: %s (%s)\n", w.HeldReason, w.HeldRemedy)
	}
	if w.ArchivePath != "" {
		_, _ = fmt.Fprintf(a.out, "archive: %s\n", w.ArchivePath)
	}
	if w.ParserVersion > 0 || w.ClaudeCodeVersion != "" {
		_, _ = fmt.Fprintf(a.out, "parser_version: %d\nclaude_code_version: %s\n", w.ParserVersion, orDash(w.ClaudeCodeVersion))
	}
	return wire.ExitOK
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func gb(n int64) string { return fmt.Sprintf("%.2f GB", float64(n)/1e9) }

// interactive reports whether standard input is a terminal.
func (a *app) interactive() bool {
	if a.isTTY != nil {
		return a.isTTY()
	}
	f, ok := a.stdin.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (a *app) printSkips(rep settle.RemoveReport) {
	if len(rep.Skipped) == 0 {
		return
	}
	_, _ = fmt.Fprintf(a.out, "skipped %d Conversation(s); counts by reason:\n", len(rep.Skipped))
	reasons := make([]string, 0, len(rep.SkipCounts))
	for r := range rep.SkipCounts {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		_, _ = fmt.Fprintf(a.out, "  %s: %d\n", r, rep.SkipCounts[r])
	}
	for _, s := range rep.Skipped {
		_, _ = fmt.Fprintf(a.out, "  %s  %s\n", s.Session, strings.Join(s.Reasons, ", "))
	}
}

// remove removes the named Conversations, or with --all-eligible every settled
// one older than remove.after. It never bypasses the age. --dry-run works while
// removal is disabled and changes nothing.
func (a *app) remove(args []string) int {
	var c common
	var dry, yes, all bool
	fs := newFlagSet("remove", a.errw)
	c.bind(fs)
	fs.BoolVar(&dry, "dry-run", false, "print what would go and how many GB; remove nothing (works while removal is disabled)")
	fs.BoolVar(&yes, "yes", false, "do not ask for confirmation")
	fs.BoolVar(&all, "all-eligible", false, "act on every eligible Conversation (needed when none is named)")
	if err := fs.Parse(reorderValue(args, valuedFlags)); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	sessions := fs.Args()
	if len(sessions) == 0 && !all && !dry {
		return a.emitError(c.jsonOut, wire.ErrUsage, "remove with no session named needs --all-eligible (or --dry-run)")
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	longRequest(cl)
	var rep settle.RemoveReport
	if err := cl.Post(a.ctx, "remove", serve.RemoveRequest{Sessions: sessions, DryRun: true}, &rep); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	if dry {
		if c.jsonOut {
			a.printJSON(rep)
			return wire.ExitOK
		}
		if !rep.Enabled {
			_, _ = fmt.Fprintln(a.out, "removal is disabled (remove.enable = false): this dry run shows what would go once it is enabled; nothing is removed")
		}
		_, _ = fmt.Fprintf(a.out, "would remove %d Conversation(s), %s\n", len(rep.Candidates), gb(rep.Bytes))
		for _, cand := range rep.Candidates {
			_, _ = fmt.Fprintf(a.out, "  %s  %d file(s)  %s\n", cand.Session, cand.Files, gb(cand.Bytes))
		}
		a.printSkips(rep)
		return wire.ExitOK
	}
	if !rep.Enabled {
		return a.emitErrorFor(c.jsonOut, wire.ErrRemovalDisabled, "removal is disabled (remove.enable = false); remove --dry-run shows what would go", cfg.Socket)
	}
	if !c.jsonOut {
		_, _ = fmt.Fprintf(a.out, "no undo except extract: %d Conversation(s), %s\n", len(rep.Candidates), gb(rep.Bytes))
	}
	if !yes {
		if !a.interactive() {
			return a.emitError(c.jsonOut, wire.ErrUsage, "remove needs --yes when it is not run interactively")
		}
		_, _ = fmt.Fprint(a.out, "remove them? [y/N] ")
		line, _ := bufio.NewReader(a.stdin).ReadString('\n')
		if ans := strings.ToLower(strings.TrimSpace(line)); ans != "y" && ans != "yes" {
			_, _ = fmt.Fprintln(a.out, "not removed")
			return wire.ExitOK
		}
	}
	var done settle.RemoveReport
	if err := cl.Post(a.ctx, "remove", serve.RemoveRequest{Sessions: sessions}, &done); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	if c.jsonOut {
		a.printJSON(done)
		return wire.ExitOK
	}
	_, _ = fmt.Fprintf(a.out, "removed %d Conversation(s)\n", len(done.Removed))
	for _, s := range done.Removed {
		_, _ = fmt.Fprintf(a.out, "  %s\n", s)
	}
	a.printSkips(done)
	return wire.ExitOK
}

// extract restores a Conversation into the Claude directory (so claude --resume
// works), or into --to <dir> without touching the Claude directory.
func (a *app) extract(args []string) int {
	var c common
	var to string
	fs := newFlagSet("extract", a.errw)
	c.bind(fs)
	fs.StringVar(&to, "to", "", "write the Conversation under this directory instead of the Claude directory")
	if err := fs.Parse(reorderValue(args, valuedFlags)); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	if fs.NArg() != 1 {
		return a.emitError(c.jsonOut, wire.ErrUsage, "extract takes one argument: a session id")
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	longRequest(cl)
	var res settle.ExtractResult
	if err := cl.Post(a.ctx, "extract", serve.ExtractRequest{Session: fs.Arg(0), To: to}, &res); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	if c.jsonOut {
		a.printJSON(res)
		return wire.ExitOK
	}
	for _, p := range res.Paths {
		_, _ = fmt.Fprintf(a.out, "restored %s\n", p)
	}
	if res.Resume != "" {
		_, _ = fmt.Fprintln(a.out, res.Resume)
	}
	return wire.ExitOK
}

// valuedFlags are the flags that take a value, so a following word is theirs.
var valuedFlags = map[string]bool{"--to": true, "--client": true, "--socket": true, "--config": true}

// reorderValue moves flags (with the value of a value flag) in front of the
// positional arguments.
func reorderValue(args []string, valued map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		x := args[i]
		switch {
		case strings.HasPrefix(x, "-") && valued[x] && i+1 < len(args):
			flags = append(flags, x, args[i+1])
			i++
		case strings.HasPrefix(x, "-"):
			flags = append(flags, x)
		default:
			pos = append(pos, x)
		}
	}
	return append(flags, pos...)
}

// doctor compares every copy with its Claude file by full hash, checks the
// journal against the index and the archives against their manifests, and exits
// 9 on any mismatch.
func (a *app) doctor(args []string) int {
	var c common
	fs := newFlagSet("doctor", a.errw)
	c.bind(fs)
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	if fs.NArg() > 0 {
		return a.emitError(c.jsonOut, wire.ErrUsage, "doctor takes no arguments")
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	longRequest(cl)
	var res settle.DoctorAnswer
	if err := cl.Post(a.ctx, "doctor", struct{}{}, &res); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	code := wire.ExitOK
	if len(res.Mismatches) > 0 {
		code = wire.ExitMismatch
	}
	if c.jsonOut {
		a.printJSON(res)
		return code
	}
	if t := res.TookOver; t != nil {
		_, _ = fmt.Fprintf(a.out, "doctor: a rolling cycle was mid-way (started %s, %s phase, %d of %d file(s) passed, %d finding(s)); this run completed it\n",
			time.Unix(0, t.StartedNS).UTC().Format(time.RFC3339), t.Phase, t.Processed, t.TotalFiles, t.Findings)
	}
	_, _ = fmt.Fprintf(a.out, "doctor: %d file(s) and %d archive(s) checked, %d file(s) with a mismatch\n", res.FilesChecked, res.ArchivesCheck, res.MismatchFiles)
	for _, m := range res.Mismatches {
		fix := ""
		if m.Repaired {
			fix = " (a new generation was started)"
		}
		_, _ = fmt.Fprintf(a.out, "  %s  %s%s: %s%s\n", m.Kind, m.Session, m.Path, m.Detail, fix)
	}
	return code
}
