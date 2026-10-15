package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// Exit codes of `status --check` (ADR 0042); the shared codes live in
// config_check.go and state.go.
const (
	exitKeeperDown = 5
	exitDegraded   = 6
)

const (
	// defaultHeartbeatIntervalS is the only fallback for the heartbeat
	// interval, used when status.json does not carry heartbeat_interval_s.
	// status never reads the configuration.
	defaultHeartbeatIntervalS = 10
	// heartbeatFile is the keeper's heartbeat file (keeper.FileHeartbeat); the
	// constant is repeated here so the client commands do not link the keeper.
	heartbeatFile = "keeper.heartbeat"
	// maxKeyWidth is where a long lease key is cut for the human table.
	maxKeyWidth = 16
)

// Keeper states reported by status.
const (
	keeperUp           = "up"
	keeperDown         = "down"
	keeperNeverStarted = "never_started"
)

// statusKeeper is the keeper block of the --json object: the status.json
// fields plus the additive "state" (up, down or never_started).
type statusKeeper struct {
	lease.StatusKeeper
	State string `json:"state"`
}

// statusLease is one lease in the --json object: the status.json fields,
// judged now rather than on the keeper's last tick.
type statusLease struct {
	lease.StatusLease
	Unreadable bool `json:"unreadable,omitempty"`
}

// statusReport is the --json object. Its keys follow the status.json schema
// (pa-monitor reads gate, assertion.held and the lease fields); the additions
// are additive and optional.
type statusReport struct {
	SchemaVersion int                    `json:"schema_version"`
	WrittenAt     time.Time              `json:"written_at"`
	Keeper        statusKeeper           `json:"keeper"`
	Assertion     lease.StatusAssertion  `json:"assertion"`
	Paused        *lease.PauseFile       `json:"paused"`
	Battery       lease.StatusBattery    `json:"battery"`
	Leases        []statusLease          `json:"leases"`
	LastRelease   *lease.StatusRelease   `json:"last_release"`
	External      []lease.StatusExternal `json:"external"`
	Unreadable    []string               `json:"unreadable,omitempty"`
	Notes         []string               `json:"notes,omitempty"`
	check         checkInputs
	hasStatusFile bool
	// leaseDetail parallels Leases: the parsed lease file, nil when unreadable.
	leaseDetail []*lease.Lease
}

// checkInputs is what `--check` needs beyond the report itself.
type checkInputs struct {
	heartbeatIntervalS int
	unexplainedTicks   int
	lingerUntil        *time.Time
	statusUnreadable   bool
}

// runStatus implements `status [--json] [--check]`.
func runStatus(args []string, stdout, stderr io.Writer) int {
	set := newFlagSet("status", stderr)
	asJSON := set.Bool("json", false, "print exactly one JSON object")
	check := set.Bool("check", false, "exit 5 when the keeper is down, 6 when degraded")
	if exit, done := parseFlags(set, args, stdout); done {
		return exit
	}
	if set.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: pg-keep-awake status [--json] [--check]")
		return exitUsage
	}
	now := time.Now().UTC()
	rep, err := buildStatus(now, stderr)
	if err != nil {
		return failState(stderr, "status", err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "pg-keep-awake: status: %v\n", err)
			return exitUnexpected
		}
	} else {
		renderStatus(stdout, rep, now)
	}
	if *check {
		return rep.checkExit(now)
	}
	return exitOK
}

// buildStatus gathers the report from status.json, assertion.json, pause.json,
// the lease files and a live process check. It never reads the configuration,
// never writes, and never deletes a file.
func buildStatus(now time.Time, stderr io.Writer) (*statusReport, error) {
	rep := &statusReport{
		SchemaVersion: lease.SchemaVersion,
		WrittenAt:     now,
		Leases:        []statusLease{},
		External:      []lease.StatusExternal{},
		Battery:       lease.StatusBattery{Gate: "unknown"},
		Keeper:        statusKeeper{State: keeperNeverStarted},
	}
	rep.check.heartbeatIntervalS = defaultHeartbeatIntervalS

	path, err := lease.ResolveStateDir()
	if err != nil {
		return nil, err
	}
	d, err := lease.CheckDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rep, nil
	}
	if err != nil {
		return nil, err
	}

	// status.json.
	st, err := d.ReadStatus()
	switch {
	case err == nil:
		rep.hasStatusFile = true
		rep.applyStatusFile(st)
	case errors.Is(err, lease.ErrUnreadable):
		rep.check.statusUnreadable = true
		rep.Unreadable = append(rep.Unreadable, lease.FileStatus)
		rep.Notes = append(rep.Notes, err.Error())
	case errors.Is(err, fs.ErrNotExist):
	default:
		fmt.Fprintf(stderr, "pg-keep-awake: status: ignoring %s: %v\n", lease.FileStatus, err)
	}

	// Keeper health: the heartbeat file's mtime is the contract; status.json's
	// own heartbeat age (aged by the time since it was written) is the
	// fallback when the file is absent.
	hbAge, haveHB := heartbeatAge(d, rep, st, now)
	if haveHB {
		rep.Keeper.HeartbeatAgeS = hbAge.Seconds()
		if hbAge > 2*time.Duration(rep.check.heartbeatIntervalS)*time.Second {
			rep.Keeper.State = keeperDown
		} else {
			rep.Keeper.State = keeperUp
		}
	}

	// Pause: pause.json when present and unexpired, else status.json's copy.
	if p, perr := d.ReadPause(); perr == nil {
		if !p.Expired(now) {
			rep.Paused = p
		}
	} else if errors.Is(perr, lease.ErrUnreadable) {
		rep.Unreadable = append(rep.Unreadable, lease.FilePause)
	}

	// Assertion: status.json says held; assertion.json plus a process check
	// says whether caffeinate is really still there.
	rep.checkAssertionProcess(d)

	// Leases: the files, judged now, merged with the keeper's last verdict.
	if err := rep.mergeLeases(d, st, now); err != nil {
		return nil, err
	}
	return rep, nil
}

func (r *statusReport) applyStatusFile(st *lease.StatusFile) {
	r.WrittenAt = st.WrittenAt
	r.Keeper.StatusKeeper = st.Keeper
	r.Assertion = st.Assertion
	r.Paused = st.Paused
	r.Battery = st.Battery
	if r.Battery.Gate == "" {
		r.Battery.Gate = "unknown"
	}
	r.LastRelease = st.LastRelease
	if st.External != nil {
		r.External = st.External
	}
	if st.HeartbeatIntervalS > 0 {
		r.check.heartbeatIntervalS = st.HeartbeatIntervalS
	}
	r.check.unexplainedTicks = st.UnexplainedTicks
	r.check.lingerUntil = st.LingerUntil
	r.Keeper.State = keeperDown
}

// heartbeatAge returns how long ago the keeper last completed a pass, and
// whether a heartbeat has ever been seen.
func heartbeatAge(d *lease.Dir, rep *statusReport, st *lease.StatusFile, now time.Time) (time.Duration, bool) {
	if fi, err := os.Lstat(filepath.Join(d.Path(), heartbeatFile)); err == nil && fi.Mode().IsRegular() {
		age := now.Sub(fi.ModTime())
		if age < 0 {
			age = 0
		}
		return age, true
	}
	if st == nil {
		return 0, false
	}
	age := time.Duration(st.Keeper.HeartbeatAgeS*float64(time.Second)) + now.Sub(st.WrittenAt)
	if age < 0 {
		age = 0
	}
	return age, true
}

// checkAssertionProcess clears Assertion.Held when assertion.json names a
// caffeinate process that is no longer there.
func (r *statusReport) checkAssertionProcess(d *lease.Dir) {
	a, err := d.ReadAssertion()
	if err != nil {
		if errors.Is(err, lease.ErrUnreadable) {
			r.Unreadable = append(r.Unreadable, lease.FileAssertion)
		}
		return
	}
	if !r.Assertion.Held {
		return
	}
	st := lease.SysProber{}.Probe(a.Caffeinate.PID)
	if !st.Exists || st.Zombie || (a.Caffeinate.StartUS != 0 && st.StartUS != a.Caffeinate.StartUS) {
		r.Assertion.Held = false
		r.Notes = append(r.Notes, fmt.Sprintf("assertion.json names caffeinate pid %d, which is gone", a.Caffeinate.PID))
	}
}

// mergeLeases lists the lease files and judges each live now.
func (r *statusReport) mergeLeases(d *lease.Dir, st *lease.StatusFile, now time.Time) error {
	scan, err := d.ListLeases()
	if err != nil {
		return err
	}
	kept := map[string]lease.StatusLease{}
	if st != nil {
		for _, l := range st.Leases {
			kept[l.ID] = l
		}
	}
	for _, l := range scan.Leases {
		sl := lease.StatusLease{
			ID: l.ID, Owner: l.Owner, Key: l.Key, Kind: l.Kind,
			ExpiresAt: l.ExpiresAt, Live: true,
		}
		if why := deadReason(l, now); why != "" {
			sl.Live, sl.Reason = false, why
		} else if prev, ok := kept[l.ID]; ok && !prev.Live {
			sl.Live, sl.Reason = false, prev.Reason
		}
		r.Leases = append(r.Leases, statusLease{StatusLease: sl})
		r.leaseDetail = append(r.leaseDetail, l)
	}
	for _, name := range scan.Unreadable {
		id := strings.TrimSuffix(name, ".json")
		r.Leases = append(r.Leases, statusLease{
			StatusLease: lease.StatusLease{ID: id, Reason: "unreadable: schema_version newer than supported"},
			Unreadable:  true,
		})
		r.leaseDetail = append(r.leaseDetail, nil)
		r.Unreadable = append(r.Unreadable, "leases/"+name)
	}
	if scan.Rejected > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d lease file(s) rejected (unparsable or invalid)", scan.Rejected))
	}
	return nil
}

// deadReason says why a lease no longer holds, or "" when it is live.
func deadReason(l *lease.Lease, now time.Time) string {
	if l.Live(now, nil) {
		return ""
	}
	if l.ExpiresAt != nil && !l.ExpiresAt.After(now) {
		return "expired"
	}
	if l.Client != nil {
		if st := (lease.SysProber{}).Probe(l.Client.PID); !st.Exists || st.StartUS != l.Client.StartUS || st.Zombie {
			return "client_gone"
		}
	}
	return "watch_gone"
}

func (r *statusReport) liveLeases() int {
	n := 0
	for _, l := range r.Leases {
		if l.Live && !l.Unreadable {
			n++
		}
	}
	return n
}

// checkExit evaluates --check: 5 when the keeper is down or never started,
// 6 when degraded, else 0. Exit 5 wins over 6.
func (r *statusReport) checkExit(now time.Time) int {
	if r.Keeper.State != keeperUp {
		return exitKeeperDown
	}
	if r.check.statusUnreadable || !r.hasStatusFile {
		return exitOK
	}
	if r.liveLeases() == 0 || r.Assertion.Held {
		return exitOK
	}
	switch r.Battery.Gate {
	case "no_spawn", "released":
		return exitOK
	}
	if r.Paused != nil {
		return exitOK
	}
	if r.check.lingerUntil != nil && r.check.lingerUntil.After(now) {
		return exitOK
	}
	if r.check.unexplainedTicks > 1 {
		return exitDegraded
	}
	return exitOK
}

// renderStatus prints the human form (see the CLI contract's sample).
func renderStatus(w io.Writer, r *statusReport, now time.Time) {
	if r.Keeper.State == keeperNeverStarted && len(r.Leases) == 0 && len(r.Unreadable) == 0 {
		fmt.Fprintln(w, "keeper: never started")
		return
	}
	age := formatAge(time.Duration(r.Keeper.HeartbeatAgeS * float64(time.Second)))
	for _, name := range r.Unreadable {
		fmt.Fprintf(w, "unreadable  %s (schema_version newer than this build)\n", name)
	}
	live := r.liveLeases()
	if len(r.Leases) == 0 && !r.Assertion.Held {
		state := "ok"
		if r.Keeper.State == keeperDown {
			state = "down"
		}
		fmt.Fprintf(w, "no leases; assertion: none; keeper: %s (%s)\n", state, age)
		renderExternal(w, r)
		renderNotes(w, r)
		return
	}

	var line string
	switch r.Keeper.State {
	case keeperDown:
		line = fmt.Sprintf("keeper   DOWN heartbeat %s", age)
	case keeperNeverStarted:
		line = "keeper   never started"
	default:
		line = fmt.Sprintf("keeper   up  heartbeat %s", age)
	}
	if r.Keeper.Version != "" {
		line += "  v" + strings.TrimPrefix(r.Keeper.Version, "v")
	}
	if r.Keeper.ConfigError != nil && *r.Keeper.ConfigError != "" {
		line += "  config error: " + *r.Keeper.ConfigError
	}
	fmt.Fprintln(w, line)
	fmt.Fprintln(w, "assert   "+assertionLine(r, live, now))

	if len(r.Leases) == 0 {
		fmt.Fprintln(w, "leases   none")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "leases   OWNER\tKEY\tCLIENT\tEXPIRES\tSTATE")
		for i, l := range r.Leases {
			fmt.Fprintf(tw, "         %s\t%s\t%s\t%s\t%s\n", dash(l.Owner), cutKey(l.Key, l.ID), clientCell(r.leaseDetail[i]),
				expiresCell(l.ExpiresAt, now), stateCell(l))
		}
		tw.Flush()
	}
	renderExternal(w, r)
	renderNotes(w, r)
}

func assertionLine(r *statusReport, live int, now time.Time) string {
	if r.Assertion.Held {
		flags := "-i"
		if len(r.Assertion.Flags) > 0 {
			flags = "-" + strings.Join(r.Assertion.Flags, " -")
		}
		s := fmt.Sprintf("HELD %s  caffeinate pid %d", flags, r.Assertion.PID)
		if b := batteryCell(r.Battery); b != "" {
			s += "  " + b
		}
		return s
	}
	s := "none"
	switch {
	case r.Paused != nil:
		s += "  (paused)"
	case r.Battery.Gate == "released" || r.Battery.Gate == "no_spawn":
		s += "  (battery gate " + r.Battery.Gate + ")"
	case r.LastRelease != nil && r.LastRelease.Reason != "":
		s += "  (last release: " + r.LastRelease.Reason
		if !r.LastRelease.At.IsZero() {
			s += " " + formatAge(now.Sub(r.LastRelease.At)) + " ago"
		}
		s += ")"
	}
	if live > 0 && r.Paused == nil && r.Battery.Gate != "released" && r.Battery.Gate != "no_spawn" {
		s += "  (live leases but none held)"
	}
	return s
}

func batteryCell(b lease.StatusBattery) string {
	if !b.ReadOK {
		return "battery unknown"
	}
	src := "AC"
	if b.OnBattery {
		src = "batt"
	}
	s := fmt.Sprintf("battery %d%% %s", b.Percent, src)
	if b.Gate != "" && b.Gate != "ok" {
		s += "  gate " + b.Gate
	}
	return s
}

func renderExternal(w io.Writer, r *statusReport) {
	for _, e := range r.External {
		fmt.Fprintf(w, "other    pid %d %s  %s  %s\n", e.PID, e.Process, e.Type,
			formatAge(time.Duration(e.AgeS*float64(time.Second))))
	}
}

func renderNotes(w io.Writer, r *statusReport) {
	for _, n := range r.Notes {
		fmt.Fprintf(w, "note     %s\n", n)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// cutKey shortens a long lease key to maxKeyWidth with a trailing "...".
func cutKey(key, id string) string {
	if key == "" {
		return dash(id)
	}
	if len(key) > maxKeyWidth {
		return key[:maxKeyWidth-3] + "..."
	}
	return key
}

func clientCell(l *lease.Lease) string {
	if l == nil || l.Client == nil {
		return "-"
	}
	if info, err := lease.ProcessInfo(l.Client.PID); err == nil && info.Name != "" {
		return fmt.Sprintf("%d (%s)", l.Client.PID, info.Name)
	}
	return fmt.Sprintf("%d", l.Client.PID)
}

func expiresCell(t *time.Time, now time.Time) string {
	if t == nil {
		return "-"
	}
	if t.After(now) {
		return "in " + formatAge(t.Sub(now))
	}
	return formatAge(now.Sub(*t)) + " ago"
}

func stateCell(l statusLease) string {
	switch {
	case l.Unreadable:
		return "unreadable"
	case l.Live:
		return "live"
	case l.Reason != "":
		return "dead (" + l.Reason + ")"
	}
	return "dead"
}

// formatAge renders a duration compactly: 4s, 3m, 3h54m, 2d4h.
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days, h := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour)
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}
