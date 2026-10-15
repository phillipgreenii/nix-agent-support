package policy

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Origin: TestParsePmsetBatt, the run-error case and the fake-reader case are
// ported from pa-monitor's internal/core/power/power_test.go. The pmset reader,
// file source and environment-selection tests are new.

func TestParsePmsetBatt(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    Reading
		wantErr bool
	}{
		{
			name: "battery discharging",
			out:  "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=27459683)\t15%; discharging; 0:41 remaining present: true\n",
			want: Reading{OnBattery: true, Percent: 15},
		},
		{
			name: "ac attached not charging",
			out:  "Now drawing from 'AC Power'\n -InternalBattery-0 (id=27459683)\t80%; AC attached; not charging present: true\n",
			want: Reading{OnBattery: false, Percent: 80},
		},
		{
			name: "ac charging",
			out:  "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged; 0:00 remaining present: true\n",
			want: Reading{OnBattery: false, Percent: 100},
		},
		{
			name: "desktop with no battery",
			out:  "Now drawing from 'AC Power'\n",
			want: Reading{OnBattery: false, Percent: NoPercent},
		},
		{
			name: "ups is not the battery",
			out:  "Now drawing from 'UPS Power'\n -CyberPower (id=2)\t60%; discharging present: true\n",
			want: Reading{OnBattery: false, Percent: NoPercent},
		},
		{
			name:    "battery power but no percentage",
			out:     "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t(no estimate); discharging present: true\n",
			wantErr: true,
		},
		{name: "garbage", out: "pmset: command exploded\n", wantErr: true},
		{name: "empty", out: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePmsetBatt(tc.out)
			if tc.wantErr {
				if !errors.Is(err, ErrUnparseable) {
					t.Fatalf("err = %v, want ErrUnparseable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFakeReader(t *testing.T) {
	f := NewFakeReader(Reading{OnBattery: true, Percent: 15})
	if r, _ := f.Read(); r != (Reading{OnBattery: true, Percent: 15}) {
		t.Errorf("Read = %+v", r)
	}
	f.SetErr(errors.New("x"))
	if _, err := f.Read(); err == nil {
		t.Error("want error after SetErr")
	}
	f.Set(Reading{Percent: 90})
	if r, err := f.Read(); err != nil || r.Percent != 90 {
		t.Errorf("Set must clear the error: %+v, %v", r, err)
	}
	if f.Reads() != 3 {
		t.Errorf("Reads = %d, want 3", f.Reads())
	}
}

func TestPmsetPathIsAbsolute(t *testing.T) {
	if PmsetPath != "/usr/bin/pmset" {
		t.Errorf("PmsetPath = %q, want the absolute /usr/bin/pmset", PmsetPath)
	}
}

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pmset")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPmsetReader_ParsesOutputOfTheConfiguredBinary(t *testing.T) {
	p := writeScript(t, `echo "Now drawing from 'Battery Power'"; echo " -InternalBattery-0 (id=1)	42%; discharging present: true"`)
	got, err := PmsetReader{Path: p}.Read()
	if err != nil || got != (Reading{OnBattery: true, Percent: 42}) {
		t.Errorf("Read = %+v, %v; want battery/42", got, err)
	}
}

func TestPmsetReader_PassesGBatt(t *testing.T) {
	// The script succeeds only for exactly `-g batt`.
	p := writeScript(t, `[ "$1 $2" = "-g batt" ] && [ $# -eq 2 ] || exit 3
echo "Now drawing from 'AC Power'"`)
	if _, err := (PmsetReader{Path: p}).Read(); err != nil {
		t.Errorf("Read = %v, want the -g batt arguments", err)
	}
}

func TestPmsetReader_NonZeroExitIsAFailedRead(t *testing.T) {
	p := writeScript(t, `echo "Now drawing from 'AC Power'"; exit 7`)
	got, err := PmsetReader{Path: p}.Read()
	if err == nil {
		t.Fatal("a non-zero exit must fail the read even when the output parses")
	}
	if got.OnBattery || got.Percent != NoPercent {
		t.Errorf("a failed read returned %+v", got)
	}
}

func TestPmsetReader_UnparsableOutputIsAFailedRead(t *testing.T) {
	p := writeScript(t, `echo "pmset: command exploded"`)
	if _, err := (PmsetReader{Path: p}).Read(); !errors.Is(err, ErrUnparseable) {
		t.Errorf("err = %v, want ErrUnparseable", err)
	}
}

func TestPmsetReader_TimesOut(t *testing.T) {
	p := writeScript(t, `exec sleep 30`)
	start := time.Now()
	_, err := PmsetReader{Path: p, Timeout: 200 * time.Millisecond}.Read()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout error", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the read took %s; the timeout did not bound it", d)
	}
}

func TestPmsetReader_MissingBinaryIsAFailedRead(t *testing.T) {
	if _, err := (PmsetReader{Path: filepath.Join(t.TempDir(), "absent")}).Read(); err == nil {
		t.Error("a missing binary must fail the read")
	}
}

// TestPmsetReader_RealPmset runs the real /usr/bin/pmset; darwin only.
func TestPmsetReader_RealPmset(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real /usr/bin/pmset exists only on darwin")
	}
	got, err := PmsetReader{}.Read()
	if err != nil {
		t.Fatalf("real pmset read failed: %v", err)
	}
	if got.OnBattery && (got.Percent < 0 || got.Percent > 100) {
		t.Errorf("on battery with percent %d", got.Percent)
	}
	if !got.OnBattery && got.Percent != NoPercent && (got.Percent < 0 || got.Percent > 100) {
		t.Errorf("percent %d out of range", got.Percent)
	}
}

func writeSource(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "battery")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFileReader(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    Reading
		wantErr bool
	}{
		{"ac", "64 ac\n", Reading{OnBattery: false, Percent: 64}, false},
		{"battery", "19 battery\n", Reading{OnBattery: true, Percent: 19}, false},
		{"no trailing newline", "19 battery", Reading{OnBattery: true, Percent: 19}, false},
		{"extra spaces", "  7   battery  \n", Reading{OnBattery: true, Percent: 7}, false},
		{"only the first line counts", "50 ac\ngarbage\n", Reading{OnBattery: false, Percent: 50}, false},
		{"zero percent", "0 battery\n", Reading{OnBattery: true, Percent: 0}, false},
		{"hundred", "100 ac\n", Reading{OnBattery: false, Percent: 100}, false},
		{"percent over 100", "101 ac\n", Reading{}, true},
		{"negative percent", "-1 ac\n", Reading{}, true},
		{"not a number", "abc battery\n", Reading{}, true},
		{"unknown source", "50 solar\n", Reading{}, true},
		{"one field", "50\n", Reading{}, true},
		{"three fields", "50 ac extra\n", Reading{}, true},
		{"empty file", "", Reading{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FileReader{Path: writeSource(t, tc.content)}.Read()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Read = %+v, want an error", got)
				}
				if got.OnBattery || got.Percent != NoPercent {
					t.Errorf("a failed read returned %+v", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("Read = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestFileReader_MissingFileIsAFailedRead(t *testing.T) {
	if _, err := (FileReader{Path: filepath.Join(t.TempDir(), "absent")}).Read(); err == nil {
		t.Error("a missing file must fail the read")
	}
}

func TestFileReader_IsReReadEveryTime(t *testing.T) {
	p := writeSource(t, "64 ac\n")
	r := FileReader{Path: p}
	if got, _ := r.Read(); got != onAC(64) {
		t.Fatalf("first read = %+v", got)
	}
	if err := os.WriteFile(p, []byte("19 battery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Read(); got != onBattery(19) {
		t.Errorf("second read = %+v, want the new content", got)
	}
}

func envOf(v string) func(string) string {
	return func(k string) string {
		if k == BatterySourceEnv {
			return v
		}
		return ""
	}
}

func TestBatterySourceEnvName(t *testing.T) {
	if BatterySourceEnv != "PG_KEEP_AWAKE_BATTERY_SOURCE" {
		t.Errorf("BatterySourceEnv = %q", BatterySourceEnv)
	}
}

func TestNewReader_UnsetUsesRealPmset(t *testing.T) {
	r, ok := NewReader(envOf("")).(PmsetReader)
	if !ok {
		t.Fatalf("NewReader(unset) = %T, want PmsetReader", NewReader(envOf("")))
	}
	if r.Path != "" {
		t.Errorf("Path = %q, want empty (the absolute default)", r.Path)
	}
}

func TestNewReader_FileSourceReplacesPmset(t *testing.T) {
	p := writeSource(t, "19 battery\n")
	r := NewReader(envOf("file:" + p))
	if got, err := r.Read(); err != nil || got != onBattery(19) {
		t.Errorf("Read = %+v, %v; want battery/19", got, err)
	}
}

func TestNewReader_ReadsTheProcessEnvironmentByDefault(t *testing.T) {
	p := writeSource(t, "64 ac\n")
	t.Setenv(BatterySourceEnv, "file:"+p)
	if got, err := NewReader(nil).Read(); err != nil || got != onAC(64) {
		t.Errorf("Read = %+v, %v; want ac/64", got, err)
	}
}

func TestNewReader_MalformedValueIsAFailedRead(t *testing.T) {
	p := writeSource(t, "19 battery\n")
	for _, v := range []string{p, "19 battery", "file:", "FILE:" + p, "http://x"} {
		if _, err := NewReader(envOf(v)).Read(); err == nil {
			t.Errorf("value %q: want a failed read", v)
		}
	}
}

// The source drives the blind rule end to end: a good battery reading, then a
// file that disappears.
func TestFileSourceDrivesTheBlindRule(t *testing.T) {
	p := writeSource(t, "25 battery\n")
	rd := NewReader(envOf("file:" + p))
	e := defaults()
	if got := e.Evaluate(rd).Gate; got != GateNoSpawn {
		t.Fatalf("gate = %v, want no_spawn", got)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	want := []Gate{GateUnknown, GateUnknown, GateReleased}
	for i, w := range want {
		if got := e.Evaluate(rd).Gate; got != w {
			t.Errorf("failed read %d: %v, want %v", i+1, got, w)
		}
	}
	if err := os.WriteFile(p, []byte("80 ac\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.Evaluate(rd).Gate; got != GateOK {
		t.Errorf("after recovery: %v, want ok", got)
	}
}
