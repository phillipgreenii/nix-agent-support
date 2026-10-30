package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// procStartLayout is the layout of the registry's procStart field and of `ps -o lstart=` output,
// ctime-style, second resolution, no zone.
const procStartLayout = "Mon Jan _2 15:04:05 2006"

// procStartTolerance absorbs a second-boundary rounding difference between the registry value
// and the live start time; a pid reused after the original exited starts far later than this.
const procStartTolerance = 2 * time.Second

// entry is one registry file, sessions/<pid>.json (only the fields the command uses).
type entry struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Cwd       string `json:"cwd"`
	ProcStart string `json:"procStart"`
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// sessionsDir is <claude dir>/sessions; the claude dir is CLAUDE_CONFIG_DIR, else ~/.claude.
func (a *app) sessionsDir() (string, error) {
	if d := a.getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "sessions"), nil
	}
	h := a.getenv("HOME")
	if h == "" {
		return "", errors.New("neither CLAUDE_CONFIG_DIR nor HOME is set")
	}
	return filepath.Join(h, ".claude", "sessions"), nil
}

// stateDir is PG_CLAUDE_CONTROL_STATE_DIR, else $XDG_STATE_HOME/pg-claude-control, else
// ~/.local/state/pg-claude-control (the same rule the mod's listener uses).
func (a *app) stateDir() (string, error) {
	if d := a.getenv("PG_CLAUDE_CONTROL_STATE_DIR"); d != "" {
		return d, nil
	}
	if x := a.getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pg-claude-control"), nil
	}
	if h := a.getenv("HOME"); h != "" {
		return filepath.Join(h, ".local", "state", "pg-claude-control"), nil
	}
	return "", errors.New("none of PG_CLAUDE_CONTROL_STATE_DIR, XDG_STATE_HOME or HOME is set")
}

// runAllow is PG_CLAUDE_CONTROL_RUN_ALLOW (comma-separated), default exit,clear,compact; exit
// always stays because stop runs it. Same rule as the mod.
func (a *app) runAllow() []string {
	var out []string
	for _, s := range strings.Split(a.getenv("PG_CLAUDE_CONTROL_RUN_ALLOW"), ",") {
		s = strings.TrimPrefix(strings.TrimSpace(s), "/")
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = []string{"exit", "clear", "compact"}
	}
	for _, s := range out {
		if s == "exit" {
			return out
		}
	}
	return append(out, "exit")
}

func (a *app) readEntry(path string) (entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return entry{}, err
	}
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return entry{}, err
	}
	return e, nil
}

// readRegistry returns every parseable registry file (unparseable ones are skipped: a half
// written file is not an error). A missing directory is an empty registry.
func (a *app) readRegistry() ([]entry, error) {
	dir, err := a.sessionsDir()
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []entry
	for _, de := range des {
		if !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		e, err := a.readEntry(filepath.Join(dir, de.Name()))
		if err != nil || e.PID <= 0 {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

func parseProcStart(s string) (time.Time, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(procStartLayout, s, time.UTC)
	return t, err == nil
}

// live reports whether the registry entry still describes a running Claude: the pid exists and
// its start time equals the entry's procStart (so a reused pid is not a Claude). A start time the
// OS cannot read because ps cannot run fails open; a ps that reports no such process does not.
func (a *app) live(e entry) bool {
	if e.PID <= 0 {
		return false
	}
	if err := a.kill(e.PID, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	want, ok := parseProcStart(e.ProcStart)
	if !ok {
		return true // an older registry without procStart: pid liveness only
	}
	res := a.runner(context.Background(), cmdrun.Call{
		Name:    "ps",
		Args:    []string{"-o", "lstart=", "-p", strconv.Itoa(e.PID)},
		Env:     []string{"TZ=UTC", "LC_ALL=C"},
		EnvMode: cmdrun.EnvAppend,
	})
	if res.StartErr != nil || res.CtxErr != nil {
		return true
	}
	if res.Code != 0 {
		return false
	}
	got, ok := parseProcStart(string(res.Stdout))
	if !ok {
		return true
	}
	d := got.Sub(want)
	if d < 0 {
		d = -d
	}
	return d <= procStartTolerance
}

// reverify re-reads the registry file for the entry and checks it is still the same running
// Claude. It runs before every write and every signal.
func (a *app) reverify(e entry) *cmdError {
	dir, err := a.sessionsDir()
	if err != nil {
		return fail(errInternal, "%v", err)
	}
	cur, err := a.readEntry(filepath.Join(dir, strconv.Itoa(e.PID)+".json"))
	if err != nil || cur.ProcStart != e.ProcStart || cur.SessionID != e.SessionID || !a.live(cur) {
		return fail(errNoProcess, "pid %d is not a running Claude", e.PID)
	}
	return nil
}

// resolve turns a <target> (pid, session id or name; pid:, session: and name: force a reading)
// into one running Claude.
func (a *app) resolve(target string) (entry, *cmdError) {
	kind, val := "", target
	for _, p := range []string{"pid", "session", "name"} {
		if strings.HasPrefix(target, p+":") {
			kind, val = p, strings.TrimPrefix(target, p+":")
		}
	}
	if kind == "" {
		switch {
		case isDigits(val):
			kind = "pid"
		case uuidShape.MatchString(val):
			kind = "session"
		default:
			kind = "name"
		}
	}
	if val == "" {
		return entry{}, fail(errUsage, "empty target %q", target)
	}
	all, err := a.readRegistry()
	if err != nil {
		return entry{}, fail(errInternal, "cannot read the session registry: %v", err)
	}
	var match []entry
	for _, e := range all {
		var ok bool
		switch kind {
		case "pid":
			ok = strconv.Itoa(e.PID) == val
		case "session":
			ok = e.SessionID == val
		default:
			ok = e.Name == val
		}
		if ok && a.live(e) {
			match = append(match, e)
		}
	}
	switch len(match) {
	case 0:
		if kind == "pid" {
			return entry{}, fail(errNoProcess, "pid %s is not a running Claude", val)
		}
		return entry{}, fail(errNoProcess, "no running Claude with %s %q", kind, val)
	case 1:
		return match[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d running Claudes match %s %q; pass pid:<n> to pick one:", len(match), kind, val)
	for _, e := range match {
		fmt.Fprintf(&b, "\npid %d session %s name %q", e.PID, e.SessionID, e.Name)
	}
	return entry{}, fail(errAmbiguous, "%s", b.String())
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
