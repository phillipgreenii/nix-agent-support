package shimargs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// alive stands in for the pid-existence probe: pids 100 and 1 exist.
func alive(pid int) bool { return pid == 100 || pid == 1 }

func TestParseRequests(t *testing.T) {
	sec := time.Second
	tests := []struct {
		name string
		args []string
		want Request
	}{
		{"no arguments", nil, Request{Flags: FlagI}},
		{"flag d", []string{"-d"}, Request{Flags: FlagD}},
		{"flag i", []string{"-i"}, Request{Flags: FlagI}},
		{"flag m", []string{"-m"}, Request{Flags: FlagM}},
		{"flag s", []string{"-s"}, Request{Flags: FlagS}},
		{"separate flags", []string{"-d", "-s"}, Request{Flags: FlagD | FlagS}},
		{"combined flags", []string{"-dims"}, Request{Flags: FlagD | FlagI | FlagM | FlagS}},
		{"-t N", []string{"-t", "300"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 300 * sec}}},
		{"attached -t300", []string{"-t300"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 300 * sec}}},
		{"attached -it300", []string{"-it300"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 300 * sec}}},
		{"-dt300", []string{"-dt300"}, Request{Flags: FlagD, Timeout: Timeout{TimeoutFinite, 300 * sec}}},
		{"-t 0 is indefinite", []string{"-t", "0"}, Request{Flags: FlagI, Timeout: Timeout{Kind: TimeoutIndefinite}}},
		{"-t with plus sign", []string{"-t", "+7"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 7 * sec}}},
		{"-t with leading space", []string{"-t", " 7"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 7 * sec}}},
		{"-t 1x numeric prefix", []string{"-t", "1x"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, sec}}},
		{"-t 1.5 numeric prefix", []string{"-t", "1.5"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, sec}}},
		{"-t 0x1 hex", []string{"-t", "0x1"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, sec}}},
		{"-t 0X1f hex", []string{"-t", "0X1f"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 31 * sec}}},
		{"-t 010 octal", []string{"-t", "010"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 8 * sec}}},
		{"-t 08 stops at 8", []string{"-t", "08"}, Request{Flags: FlagI, Timeout: Timeout{Kind: TimeoutIndefinite}}},
		{"-t 0x without digits is 0", []string{"-t", "0x"}, Request{Flags: FlagI, Timeout: Timeout{Kind: TimeoutIndefinite}}},
		{"repeated -t last wins", []string{"-t", "1", "-t", "2"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 2 * sec}}},
		{"repeated -t negative then valid", []string{"-t", "-5", "-t", "2"}, Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 2 * sec}}},
		{"repeated -t 0 last", []string{"-t", "2", "-t", "0"}, Request{Flags: FlagI, Timeout: Timeout{Kind: TimeoutIndefinite}}},
		{"-w PID", []string{"-w", "100"}, Request{Flags: FlagI, Watch: Watch{WatchPID, 100}}},
		{"attached -w100", []string{"-w100"}, Request{Flags: FlagI, Watch: Watch{WatchPID, 100}}},
		{"-w 0 is indefinite", []string{"-w", "0"}, Request{Flags: FlagI, Watch: Watch{Kind: WatchIndefinite}}},
		{"-w 0x64 hex", []string{"-w", "0x64"}, Request{Flags: FlagI, Watch: Watch{WatchPID, 100}}},
		{"-w truncates to 32 bits", []string{"-w", "4294967297"}, Request{Flags: FlagI, Watch: Watch{WatchPID, 1}}},
		{"-w truncating to 0 is indefinite", []string{"-w", "4294967296"}, Request{Flags: FlagI, Watch: Watch{Kind: WatchIndefinite}}},
		{"repeated -w last wins", []string{"-w", "-1", "-w", "100"}, Request{Flags: FlagI, Watch: Watch{WatchPID, 100}}},
		{"repeated -w nonexistent then 0", []string{"-w", "-1", "-w", "0"}, Request{Flags: FlagI, Watch: Watch{Kind: WatchIndefinite}}},
		{
			"-t and -w together",
			[]string{"-t", "30", "-w", "100"},
			Request{Flags: FlagI, Timeout: Timeout{TimeoutFinite, 30 * sec}, Watch: Watch{WatchPID, 100}},
		},
		{
			"flags then -t and -w",
			[]string{"-s", "-t30", "-w100"},
			Request{Flags: FlagS, Timeout: Timeout{TimeoutFinite, 30 * sec}, Watch: Watch{WatchPID, 100}},
		},
		{"-- alone", []string{"--"}, Request{Flags: FlagI}},
		{"flags then --", []string{"-d", "--"}, Request{Flags: FlagD}},
		{"utility form", []string{"sleep", "5"}, Request{Flags: FlagI, Utility: []string{"sleep", "5"}}},
		{"flags then utility", []string{"-ds", "sleep", "5"}, Request{Flags: FlagD | FlagS, Utility: []string{"sleep", "5"}}},
		{"utility options are not parsed", []string{"-i", "echo", "-z", "-t"}, Request{Flags: FlagI, Utility: []string{"echo", "-z", "-t"}}},
		{"-- ends options", []string{"--", "-t", "1"}, Request{Flags: FlagI, Utility: []string{"-t", "1"}}},
		{"lone dash is a utility", []string{"-"}, Request{Flags: FlagI, Utility: []string{"-"}}},
		{"utility ignores -t", []string{"-t", "5", "echo", "hi"}, Request{Flags: FlagI, Utility: []string{"echo", "hi"}}},
		{"utility ignores negative -t", []string{"-t", "-5", "echo", "hi"}, Request{Flags: FlagI, Utility: []string{"echo", "hi"}}},
		{"utility ignores overflowing -t", []string{"-t99999999999999999999", "echo", "hi"}, Request{Flags: FlagI, Utility: []string{"echo", "hi"}}},
		{"utility with -w on a dead pid still runs", []string{"-w", "99999999", "echo", "hi"}, Request{Flags: FlagI, Utility: []string{"echo", "hi"}}},
		{"utility with -w -1 still runs", []string{"-w", "-1", "echo", "hi"}, Request{Flags: FlagI, Utility: []string{"echo", "hi"}}},
		{"utility ignores -w 100", []string{"-w", "100", "echo", "hi"}, Request{Flags: FlagI, Utility: []string{"echo", "hi"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, out := Parse(tc.args, alive)
			if out != nil {
				t.Fatalf("Parse(%q) outcome %+v, want request", tc.args, out)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q)\n got %+v\nwant %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestParseOutcomes(t *testing.T) {
	const (
		missingT  = "caffeinate: option requires an argument -- t\n" + UsageLine
		missingW  = "caffeinate: option requires an argument -- w\n" + UsageLine
		illegalZ  = "caffeinate: illegal option -- z\n" + UsageLine
		rejectedU = rejectedUMessage
		usageOnly = UsageLine
		silent    = ""
	)
	tests := []struct {
		name   string
		args   []string
		kind   OutcomeKind
		stderr string
		exit   int
	}{
		{"-u rejected", []string{"-u"}, OutcomeRejectedU, rejectedU, 1},
		{"-u in a combination", []string{"-iu", "-t1"}, OutcomeRejectedU, rejectedU, 1},
		{"-u with a utility", []string{"-u", "echo"}, OutcomeRejectedU, rejectedU, 1},
		{"unknown flag", []string{"-z"}, OutcomeIllegalOption, illegalZ, 1},
		{"unknown flag after valid flags", []string{"-d", "-z"}, OutcomeIllegalOption, illegalZ, 1},
		{"unknown flag in a combination", []string{"-dz"}, OutcomeIllegalOption, illegalZ, 1},
		{"unknown flag wins over -u", []string{"-u", "-z"}, OutcomeIllegalOption, illegalZ, 1},
		{"unknown flag before utility", []string{"-z", "echo", "hi"}, OutcomeIllegalOption, illegalZ, 1},
		{"-t negative", []string{"-t", "-5"}, OutcomeNoLeaseTime, silent, 0},
		{"-t negative attached", []string{"-t-5"}, OutcomeNoLeaseTime, silent, 0},
		{"-t negative last wins", []string{"-t", "2", "-t", "-5"}, OutcomeNoLeaseTime, silent, 0},
		{"-t overflow", []string{"-t", "99999999999999999999"}, OutcomeNoLeaseTime, silent, 0},
		{"-t LONG_MAX", []string{"-t", "9223372036854775807"}, OutcomeNoLeaseTime, silent, 0},
		{"-t LONG_MAX plus one", []string{"-t", "9223372036854775808"}, OutcomeNoLeaseTime, silent, 0},
		{"-t negative overflow", []string{"-t", "-9223372036854775809"}, OutcomeNoLeaseTime, silent, 0},
		{"-t hex LONG_MAX", []string{"-t", "0x7fffffffffffffff"}, OutcomeNoLeaseTime, silent, 0},
		{"-t beyond duration range", []string{"-t", "9223372037"}, OutcomeNoLeaseTime, silent, 0},
		{"-t abc", []string{"-t", "abc"}, OutcomeUsage, usageOnly, 1},
		{"-t empty", []string{"-t", ""}, OutcomeUsage, usageOnly, 1},
		{"-t sign only", []string{"-t", "-"}, OutcomeUsage, usageOnly, 1},
		{"-t abc with utility", []string{"-t", "abc", "echo", "hi"}, OutcomeUsage, usageOnly, 1},
		{"-t abc wins over earlier valid -t", []string{"-t1", "-t", "abc"}, OutcomeUsage, usageOnly, 1},
		{"-w abc", []string{"-w", "abc"}, OutcomeUsage, usageOnly, 1},
		{"-ww", []string{"-ww"}, OutcomeUsage, usageOnly, 1},
		{"-t without value", []string{"-t"}, OutcomeMissingArg, missingT, 1},
		{"-w without value", []string{"-w"}, OutcomeMissingArg, missingW, 1},
		{"-dt without value", []string{"-dt"}, OutcomeMissingArg, missingT, 1},
		{"-w -1", []string{"-w", "-1"}, OutcomeNoLeasePID, silent, 0},
		{"-w negative", []string{"-w", "-5"}, OutcomeNoLeasePID, silent, 0},
		{"-w nonexistent", []string{"-w", "99999999"}, OutcomeNoLeasePID, silent, 0},
		{"-w beyond 32 bits negative", []string{"-w", "2147483648"}, OutcomeNoLeasePID, silent, 0},
		{"-w LONG_MAX", []string{"-w", "9223372036854775807"}, OutcomeNoLeasePID, silent, 0},
		{"-w last wins nonexistent", []string{"-w", "100", "-w", "-1"}, OutcomeNoLeasePID, silent, 0},
		{"-t with -w nonexistent", []string{"-t", "30", "-w", "99999999"}, OutcomeNoLeasePID, silent, 0},
		{"-t negative with -w", []string{"-t", "-1", "-w", "100"}, OutcomeNoLeaseTime, silent, 0},
		{"-u wins over no-lease", []string{"-u", "-t", "-5"}, OutcomeRejectedU, rejectedU, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, out := Parse(tc.args, alive)
			if out == nil {
				t.Fatalf("Parse(%q) = request %+v, want outcome", tc.args, req)
			}
			if !reflect.DeepEqual(req, Request{}) {
				t.Errorf("request must be zero with an outcome, got %+v", req)
			}
			if out.Kind != tc.kind || out.Stderr != tc.stderr || out.ExitCode != tc.exit {
				t.Errorf("Parse(%q) = %+v, want kind %d stderr %q exit %d", tc.args, *out, tc.kind, tc.stderr, tc.exit)
			}
		})
	}
}

func TestParseNilProbeTreatsPidsAsPresent(t *testing.T) {
	req, out := Parse([]string{"-w", "99999999"}, nil)
	if out != nil || req.Watch != (Watch{WatchPID, 99999999}) {
		t.Errorf("got %+v, %+v", req, out)
	}
}

func TestParseProbeNotCalledForUtilityForm(t *testing.T) {
	called := false
	_, out := Parse([]string{"-w", "7", "echo"}, func(int) bool { called = true; return false })
	if out != nil || called {
		t.Errorf("utility form must not probe the pid (outcome %+v, called %v)", out, called)
	}
}

func TestParseDoesNotAliasInput(t *testing.T) {
	args := []string{"-s", "echo", "hi"}
	req, _ := Parse(args, alive)
	req.Utility[0] = "changed"
	if args[1] != "echo" {
		t.Errorf("Request.Utility aliases the caller's slice: %q", args)
	}
}

func TestStrtol(t *testing.T) {
	const max, min = int64(1<<63 - 1), int64(-1 << 63)
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"7", 7, true},
		{"-7", -7, true},
		{"+7", 7, true},
		{"  \t\n7", 7, true},
		{"1x", 1, true},
		{"1.5", 1, true},
		{"1,5", 1, true},
		{"0x1", 1, true},
		{"0XfF", 255, true},
		{"-0x10", -16, true},
		{"0x", 0, true},
		{"0xg", 0, true},
		{"010", 8, true},
		{"08", 0, true},
		{"0778", 63, true},
		{"", 0, false},
		{"abc", 0, false},
		{"-", 0, false},
		{"+", 0, false},
		{" ", 0, false},
		{"x1", 0, false},
		{"9223372036854775807", max, true},
		{"9223372036854775808", max, true},
		{"99999999999999999999", max, true},
		{"-9223372036854775808", min, true},
		{"-9223372036854775809", min, true},
		{"-99999999999999999999", min, true},
		{"0x7fffffffffffffff", max, true},
		{"0x8000000000000000", max, true},
		{"0xffffffffffffffffff", max, true},
	}
	for _, tc := range tests {
		got, ok := strtol(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("strtol(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFlags(t *testing.T) {
	if got := (FlagS | FlagD | FlagM | FlagI).String(); got != "dims" {
		t.Errorf("String = %q", got)
	}
	if got := Flags(0).String(); got != "" {
		t.Errorf("empty String = %q", got)
	}
	if !(FlagD | FlagS).Has(FlagS) || FlagD.Has(FlagS) {
		t.Error("Has wrong")
	}
}

// TestParityWithRealCaffeinate runs every command line that yields an outcome
// against /usr/bin/caffeinate and compares exit status and stderr. Outcomes
// never hold an assertion, so the real tool returns at once. The shim-only -u
// rejection is skipped. Command lines that yield a request are skipped too
// (the real tool would hold them); the shim program's own parity test covers
// the runtime rows.
func TestParityWithRealCaffeinate(t *testing.T) {
	const realBin = "/usr/bin/caffeinate"
	if _, err := os.Stat(realBin); err != nil {
		t.Skipf("%s not present", realBin)
	}
	lines := [][]string{
		{"-z"},
		{"-d", "-z"},
		{"-dz"},
		{"-z", "echo", "hi"},
		{"-t", "-5"},
		{"-t-5"},
		{"-t", "2", "-t", "-5"},
		{"-t", "99999999999999999999"},
		{"-t", "9223372036854775807"},
		{"-t", "-9223372036854775809"},
		{"-t", "0x7fffffffffffffff"},
		{"-t", "abc"},
		{"-t", ""},
		{"-t", "-"},
		{"-t1", "-t", "abc"},
		{"-w", "abc"},
		{"-ww"},
		{"-t"},
		{"-w"},
		{"-dt"},
		{"-w", "-1"},
		{"-w", "-5"},
		{"-w", "99999999"},
		{"-w", "2147483648"},
		{"-w", "9223372036854775807"},
		{"-w", "1", "-w", "-1"},
		{"-t", "30", "-w", "99999999"},
		{"-t", "-1", "-w", "99999999"},
	}
	for _, args := range lines {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, out := Parse(args, func(pid int) bool { return pid == 1 })
			if out == nil || out.Kind == OutcomeRejectedU {
				t.Skip("Parse yields a request or the shim-only -u rejection")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, realBin, args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			gotStderr := strings.ReplaceAll(stderr.String(), realBin+":", "caffeinate:")
			if code != out.ExitCode || gotStderr != out.Stderr {
				t.Errorf("real: exit %d stderr %q; shimargs: exit %d stderr %q", code, gotStderr, out.ExitCode, out.Stderr)
			}
		})
	}
}
