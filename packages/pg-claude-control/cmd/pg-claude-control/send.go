package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// maxLine is the wire cap: 512 bytes including the newline (macOS's PIPE_BUF, so one write is
// atomic and two writers cannot interleave a request).
const maxLine = 512

const sentUnconfirmed = "sent; effect not confirmed"

// req is one request to put on the wire.
type req struct {
	verb string
	arg  string
	// payloadText, when non-nil, travels in a payload file (submit only).
	payloadText *string
	replyPath   string
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func buildLine(id string, r req, payloadPath string) string {
	parts := []string{"id=" + id, r.verb}
	if r.arg != "" {
		parts = append(parts, r.arg)
	}
	if payloadPath != "" {
		parts = append(parts, "payload="+payloadPath)
	}
	if r.replyPath != "" {
		parts = append(parts, "reply="+r.replyPath)
	}
	return strings.Join(parts, " ") + "\n"
}

// reply is the mod's answer file.
type reply struct {
	OK     *bool           `json:"ok"`
	Result json.RawMessage `json:"result"`
	Reason string          `json:"reason"`
}

func readReply(path string) (reply, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return reply{}, false
	}
	var r reply
	if json.Unmarshal(raw, &r) != nil || r.OK == nil {
		return reply{}, false
	}
	return r, true
}

// pause sleeps through the injectable clock (time.Sleep unless a test replaced it).
func (a *app) pause(d time.Duration) {
	if a.sleep != nil {
		a.sleep(d)
		return
	}
	time.Sleep(d)
}

// inboxRetryStep is the pause between ENOENT retries. The wait is counted in these steps, not
// read from the wall clock, so the retry is deterministic under an injected sleep.
const inboxRetryStep = 50 * time.Millisecond

// openInbox opens <pid>.fifo non-blocking for writing. ENOENT is retried for up to wait (the
// listener recreates a healed FIFO within seconds), then exit 4; ENXIO (nobody reading) is exit 5.
func (a *app) openInbox(e entry, state string, wait time.Duration) (int, string, *cmdError) {
	path := filepath.Join(state, "inbox", strconv.Itoa(e.PID)+".fifo")
	var waited time.Duration
	announced := false
	for {
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		switch {
		case err == nil:
			var st syscall.Stat_t
			if serr := syscall.Fstat(fd, &st); serr != nil || st.Mode&syscall.S_IFMT != syscall.S_IFIFO {
				_ = syscall.Close(fd)
				return -1, path, fail(errInternal, "%s is not a FIFO", path)
			}
			return fd, path, nil
		case errors.Is(err, syscall.ENOENT):
			if waited >= wait {
				return -1, path, fail(errNoControlMod,
					"Claude pid %d (session %s) has no control mod: its inbox %s does not exist; "+
						"enable the pg-claude-control mod and restart that Claude, or end it with: "+
						"pg-claude-control stop pid:%d --signal", e.PID, e.SessionID, path, e.PID)
			}
			if !announced {
				announced = true
				_, _ = fmt.Fprintln(a.errw, "waiting for inbox...")
			}
			a.pause(inboxRetryStep)
			waited += inboxRetryStep
		case errors.Is(err, syscall.ENXIO):
			return -1, path, notAccepting(e)
		default:
			return -1, path, fail(errInternal, "cannot open %s: %v", path, err)
		}
	}
}

func notAccepting(e entry) *cmdError {
	return fail(errNotAccepting,
		"Claude pid %d is not reading its inbox (no listener, or its buffer is full); "+
			"try: pg-claude-control stop pid:%d --escalate 10s", e.PID, e.PID)
}

func writeFD(fd int, e entry, line string) *cmdError {
	n, err := syscall.Write(fd, []byte(line))
	switch {
	case err == nil && n == len(line):
		return nil
	case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EPIPE):
		return notAccepting(e)
	case err != nil:
		return fail(errInternal, "write to the inbox failed: %v", err)
	}
	return fail(errInternal, "short write to the inbox (%d of %d bytes)", n, len(line))
}

// deliver re-verifies the target, opens the inbox and writes one request. It returns the id.
func (a *app) deliver(e entry, state string, r req, o opts) (string, *cmdError) {
	if ce := a.reverify(e); ce != nil {
		return "", ce
	}
	fd, _, ce := a.openInbox(e, state, o.wait)
	if ce != nil {
		return "", ce
	}
	defer func() { _ = syscall.Close(fd) }()
	id := newID()
	payloadPath := ""
	line := buildLine(id, r, payloadPath)
	if r.payloadText != nil {
		if len(line) > maxLine || strings.ContainsAny(*r.payloadText, "\n\r") {
			dir := filepath.Join(state, "inbox", "payloads")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", fail(errInternal, "cannot create %s: %v", dir, err)
			}
			payloadPath = filepath.Join(dir, id+".txt")
			if err := os.WriteFile(payloadPath, []byte(*r.payloadText), 0o600); err != nil {
				return "", fail(errInternal, "cannot write the payload: %v", err)
			}
			r.arg = ""
			line = buildLine(id, r, payloadPath)
		}
	}
	if len(line) > maxLine {
		if payloadPath != "" {
			_ = os.Remove(payloadPath)
		}
		return "", fail(errUsage, "the request is %d bytes; the wire allows %d", len(line), maxLine)
	}
	if ce := writeFD(fd, e, line); ce != nil {
		if payloadPath != "" {
			_ = os.Remove(payloadPath)
		}
		return "", ce
	}
	return id, nil
}

// awaitReply polls the reply file until valid JSON arrives or the timeout passes.
func (a *app) awaitReply(path string, timeout time.Duration) (reply, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if r, ok := readReply(path); ok {
			return r, true
		}
		if time.Now().After(deadline) {
			return reply{}, false
		}
		a.pause(20 * time.Millisecond)
	}
}

func (a *app) command(name string, args []string) int {
	maxPos, verbatim := 1, false
	switch name {
	case "list":
		maxPos = 0
	case "run":
		maxPos, verbatim = 2, true
	}
	pos, tail, o, ce := a.parseArgs(name, args, maxPos, verbatim, name == "stop")
	if ce == nil && len(pos) < maxPos {
		ce = fail(errUsage, "%s needs %d argument(s)\n%s", name, maxPos, usageText)
	}
	if ce != nil {
		return a.emitError(false, ce)
	}
	if name == "list" {
		return a.list(o)
	}
	if name == "stop" && o.signal && o.escalate > 0 {
		return a.emitError(o.json, fail(errUsage, "--signal and --escalate exclude each other"))
	}
	// client-side pre-checks, before anything is resolved or written
	var r req
	switch name {
	case "submit":
		b, err := io.ReadAll(a.stdin)
		if err != nil {
			return a.emitError(o.json, fail(errInternal, "cannot read standard input: %v", err))
		}
		text := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
		if text == "" {
			return a.emitError(o.json, fail(errUsage, "submit needs the text on standard input"))
		}
		if strings.HasPrefix(text, "/") {
			return a.emitError(o.json, fail(errUsage, "submit refuses text starting with /; use run for slash commands"))
		}
		r = req{verb: "submit", arg: text, payloadText: &text}
	case "interrupt":
		r = req{verb: "interrupt"}
	case "read":
		r = req{verb: "read"}
		o.reply = true
	case "run":
		cmd := strings.TrimPrefix(pos[1], "/")
		ok := false
		for _, n := range a.runAllow() {
			if n == cmd {
				ok = true
			}
		}
		if !ok {
			return a.emitError(o.json, fail(errUsage, "run refuses %q: not in the allowlist (%s)", cmd, strings.Join(a.runAllow(), ", ")))
		}
		r = req{verb: "run", arg: strings.TrimSpace(strings.Join(append([]string{cmd}, tail...), " "))}
	}
	e, ce := a.resolve(pos[0])
	if ce != nil {
		return a.emitError(o.json, ce)
	}
	if name == "stop" && o.signal {
		return a.stopSignal(e, o, "")
	}
	state, err := a.stateDir()
	if err != nil {
		return a.emitError(o.json, fail(errInternal, "%v", err))
	}
	if name == "stop" {
		return a.stop(e, state, o)
	}
	rdir := ""
	if o.reply {
		rdir, err = os.MkdirTemp("", "pg-claude-control-reply-")
		if err != nil {
			return a.emitError(o.json, fail(errInternal, "cannot create a reply directory: %v", err))
		}
		defer func() { _ = os.RemoveAll(rdir) }()
		r.replyPath = filepath.Join(rdir, "reply.json")
	}
	id, ce := a.deliver(e, state, r, o)
	if ce != nil {
		return a.emitError(o.json, ce)
	}
	if r.replyPath == "" {
		return a.report(o, e, id, false, nil, sentUnconfirmed)
	}
	rep, ok := a.awaitReply(r.replyPath, o.timeout)
	if !ok {
		return a.emitError(o.json, fail(errNoReply, "pid %d did not answer within %s", e.PID, o.timeout))
	}
	if !*rep.OK {
		return a.emitError(o.json, fail(errRefused, "%s", rep.Reason))
	}
	if name == "read" && !o.json {
		var res struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(rep.Result, &res)
		_, _ = fmt.Fprintln(a.out, res.Text)
		return exitOK
	}
	return a.report(o, e, id, true, rep.Result, "sent; confirmed by the mod")
}

func (a *app) report(o opts, e entry, id string, confirmed bool, result json.RawMessage, msg string) int {
	if o.json {
		b, _ := json.Marshal(struct {
			Sent      bool            `json:"sent"`
			Confirmed bool            `json:"confirmed"`
			PID       int             `json:"pid"`
			Session   string          `json:"session"`
			ID        string          `json:"id,omitempty"`
			Result    json.RawMessage `json:"result,omitempty"`
		}{true, confirmed, e.PID, e.SessionID, id, result})
		_, _ = fmt.Fprintln(a.out, string(b))
		return exitOK
	}
	_, _ = fmt.Fprintln(a.out, msg)
	return exitOK
}

// stopSignal sends SIGTERM (never SIGKILL) after re-verifying the pid still is that Claude.
func (a *app) stopSignal(e entry, o opts, id string) int {
	if ce := a.reverify(e); ce != nil {
		return a.emitError(o.json, ce)
	}
	if err := a.kill(e.PID, syscall.SIGTERM); err != nil {
		return a.emitError(o.json, fail(errInternal, "cannot signal pid %d: %v", e.PID, err))
	}
	return a.report(o, e, id, false, nil, fmt.Sprintf("sent SIGTERM to pid %d", e.PID))
}

// stop aborts a running turn, then runs exit; with --escalate it follows with SIGTERM if the
// Claude is still alive after the duration.
func (a *app) stop(e entry, state string, o opts) int {
	if _, ce := a.deliver(e, state, req{verb: "interrupt"}, o); ce != nil {
		return a.emitError(o.json, a.stopHint(ce, e))
	}
	rdir, err := os.MkdirTemp("", "pg-claude-control-reply-")
	if err != nil {
		return a.emitError(o.json, fail(errInternal, "cannot create a reply directory: %v", err))
	}
	defer func() { _ = os.RemoveAll(rdir) }()
	rp := filepath.Join(rdir, "reply.json")
	id, ce := a.deliver(e, state, req{verb: "run", arg: "exit", replyPath: rp}, o)
	if ce != nil {
		return a.emitError(o.json, a.stopHint(ce, e))
	}
	limit := o.timeout
	if o.escalate > 0 {
		limit = o.escalate
	}
	deadline := time.Now().Add(limit)
	for {
		if rep, ok := readReply(rp); ok && !*rep.OK {
			return a.emitError(o.json, fail(errRefused, "%s", rep.Reason))
		}
		if !a.live(e) {
			return a.report(o, e, id, true, nil, fmt.Sprintf("pid %d stopped", e.PID))
		}
		if time.Now().After(deadline) {
			break
		}
		a.pause(a.pollEvery())
	}
	if o.escalate > 0 {
		return a.stopSignal(e, o, id)
	}
	if _, ok := readReply(rp); ok {
		return a.report(o, e, id, true, nil, "sent; confirmed by the mod")
	}
	if o.reply {
		return a.emitError(o.json, fail(errNoReply, "pid %d did not answer within %s", e.PID, o.timeout))
	}
	return a.report(o, e, id, false, nil, sentUnconfirmed)
}

func (a *app) stopHint(ce *cmdError, e entry) *cmdError {
	if ce.name == errNoControlMod {
		return fail(errNoControlMod, "%s", ce.msg)
	}
	return ce
}

func (a *app) list(o opts) int {
	all, err := a.readRegistry()
	if err != nil {
		return a.emitError(o.json, fail(errInternal, "cannot read the session registry: %v", err))
	}
	state, serr := a.stateDir()
	type row struct {
		PID     int    `json:"pid"`
		Session string `json:"session"`
		Name    string `json:"name"`
		Cwd     string `json:"cwd"`
		Inbox   bool   `json:"inbox"`
	}
	rows := []row{}
	for _, e := range all {
		if !a.live(e) {
			continue
		}
		has := false
		if serr == nil {
			if st, err := os.Lstat(filepath.Join(state, "inbox", strconv.Itoa(e.PID)+".fifo")); err == nil && st.Mode()&os.ModeNamedPipe != 0 {
				has = true
			}
		}
		rows = append(rows, row{e.PID, e.SessionID, e.Name, e.Cwd, has})
	}
	if o.json {
		b, _ := json.Marshal(rows)
		_, _ = fmt.Fprintln(a.out, string(b))
		return exitOK
	}
	for _, r := range rows {
		inbox := "no"
		if r.Inbox {
			inbox = "yes"
		}
		_, _ = fmt.Fprintf(a.out, "%d\t%s\t%s\tinbox=%s\n", r.PID, r.Session, r.Name, inbox)
	}
	return exitOK
}
