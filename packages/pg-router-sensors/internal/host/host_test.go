package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/netprobe"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
	"github.com/phillipgreenii/pg-router/schemas"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseLoadAvg(t *testing.T) {
	v, err := ParseLoadAvg(fixture(t, "loadavg.txt"))
	if err != nil || v != 11.02 {
		t.Fatalf("got %v, %v", v, err)
	}
	for _, bad := range []string{"", "{ }", "{ x 1 2 }", "{ -1 0 0 }", "garbage"} {
		if _, err := ParseLoadAvg(bad); err == nil {
			t.Errorf("ParseLoadAvg(%q) should fail", bad)
		}
	}
}

func TestParseSwapUsedGiB(t *testing.T) {
	v, err := ParseSwapUsedGiB(fixture(t, "swapusage.txt"))
	if err != nil || v < 3.71 || v > 3.72 {
		t.Fatalf("got %v, %v", v, err)
	}
	for in, want := range map[string]float64{
		"total = 1G  used = 2.5G  free = 0G":    2.5,
		"total = 1G  used = 2048M  free = 0G":   2,
		"total = 1G  used = 1048576K free = 0G": 1,
		"total = 1G  used = 1T  free = 0G":      1024,
	} {
		if got, err := ParseSwapUsedGiB(in); err != nil || got != want {
			t.Errorf("ParseSwapUsedGiB(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseSwapUsedGiB(fixture(t, "pressure-garbage.txt")); err == nil {
		t.Error("garbage must fail")
	}
}

func TestParsePressure(t *testing.T) {
	for file, want := range map[string]string{"pressure-1.txt": "PLENTY", "pressure-2.txt": "OK", "pressure-4.txt": "EXHAUSTED"} {
		if got, err := ParsePressure(fixture(t, file)); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
	for _, bad := range []string{fixture(t, "pressure-garbage.txt"), "", "3", "0"} {
		if _, err := ParsePressure(bad); err == nil {
			t.Errorf("ParsePressure(%q) should fail", bad)
		}
	}
}

// rig wires a Sensor to a fake command runner and a fake statfs.
type rig struct {
	fake       *cmdruntest.Fake
	s          *Sensor
	free       map[string]float64 // GiB per path; absent path is unreadable
	load       string
	ncpu       string
	swapG      float64
	press      string
	swapRaw    string         // when set, the raw swapusage output
	sysctlFail *cmdrun.Result // when set, every sysctl returns it
	state      string
	now        time.Time
	out        bytes.Buffer
	errb       bytes.Buffer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		fake: cmdruntest.New(t), free: map[string]float64{"/": 100},
		load: "{ 1.00 1.00 1.00 }\n", ncpu: "10\n", swapG: 0, press: "1\n",
		state: t.TempDir(), now: t0,
	}
	netSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(netSrv.Close)
	r.s = &Sensor{
		Run: r.fake.Runner(), Paths: []string{"/"}, T: Defaults(),
		Net: &netprobe.Prober{Client: netSrv.Client(), Timeout: time.Second}, NetTargets: []string{netSrv.URL},
	}
	r.s.Statfs = func(p string) (uint64, error) {
		g, ok := r.free[p]
		if !ok {
			return 0, errors.New("no such path")
		}
		return uint64(g * gib), nil
	}
	r.fake.On(func(cmdrun.Call) bool { return r.sysctlFail != nil }).Respond(func(cmdrun.Call) cmdrun.Result { return *r.sysctlFail })
	r.fake.On(cmdruntest.Match("sysctl", "-n", SysctlLoad)).Respond(func(cmdrun.Call) cmdrun.Result { return cmdruntest.Out(r.load) })
	r.fake.On(cmdruntest.Match("sysctl", "-n", SysctlNCPU)).Respond(func(cmdrun.Call) cmdrun.Result { return cmdruntest.Out(r.ncpu) })
	r.fake.On(cmdruntest.Match("sysctl", "-n", SysctlSwap)).Respond(func(cmdrun.Call) cmdrun.Result {
		if r.swapRaw != "" {
			return cmdruntest.Out(r.swapRaw)
		}
		return cmdruntest.Out("total = 99999.00M  used = " + strings.TrimSpace(formatM(r.swapG)) + "M  free = 1.00M  (encrypted)\n")
	})
	r.fake.On(cmdruntest.Match("sysctl", "-n", SysctlPressure)).Respond(func(cmdrun.Call) cmdrun.Result { return cmdruntest.Out(r.press) })
	return r
}

func formatM(g float64) string {
	b, _ := json.Marshal(g * 1024)
	return string(b)
}

// run invokes the real dispatch (sensorcli.Run) so state, dwell and schema
// validation are in the loop. extra are further args (e.g. --raw).
func (r *rig) run(t *testing.T, extra ...string) (int, []map[string]any) {
	t.Helper()
	r.out.Reset()
	r.errb.Reset()
	args := append([]string{"host-sensor", "--state-dir", r.state}, extra...)
	code := sensorcli.Run(args, sensorcli.Registry{"host-sensor": r.s}, sensorcli.IO{
		Stdout: &r.out, Stderr: &r.errb, Getenv: func(string) string { return "" }, Now: func() time.Time { return r.now },
	})
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(r.out.String()), "\n") {
		if l == "" {
			continue
		}
		if err := schemas.ValidateLevelAnswer([]byte(l)); err != nil {
			t.Fatalf("line %q fails the schema: %v", l, err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	return code, lines
}

func byRes(lines []map[string]any, res string) map[string]any {
	for _, l := range lines {
		if l["resource"] == res {
			return l
		}
	}
	return nil
}

func (r *rig) level(t *testing.T, res string) string {
	t.Helper()
	code, ls := r.run(t, "--raw", "--resource", res)
	if code != 0 || len(ls) != 1 {
		t.Fatalf("%s: exit %d lines %v stderr %s", res, code, ls, r.errb.String())
	}
	return ls[0]["level"].(string)
}

func TestDiskBandsIncludeLowerBoundExcludeUpper(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		gib  float64
		want string
	}{{200, "PLENTY"}, {40, "PLENTY"}, {39.99, "OK"}, {25, "OK"}, {24.99, "TIGHT"}, {12, "TIGHT"}, {11.99, "EXHAUSTED"}, {0, "EXHAUSTED"}} {
		r.free["/"] = c.gib
		if got := r.level(t, "disk"); got != c.want {
			t.Errorf("disk %v GiB = %s, want %s", c.gib, got, c.want)
		}
	}
}

func TestDiskIsLowestOverConfiguredPathsAndSkipsUnreadable(t *testing.T) {
	r := newRig(t)
	r.s.Paths = []string{"/", "/data", "/missing"}
	r.free = map[string]float64{"/": 80, "/data": 30}
	code, ls := r.run(t, "--raw", "--resource", "disk")
	if code != 0 || ls[0]["level"] != "OK" || ls[0]["reading"].(map[string]any)["value"].(float64) != 30 {
		t.Fatalf("got %d %v", code, ls)
	}
	if !strings.Contains(r.errb.String(), "/missing") {
		t.Errorf("skipped path not reported: %s", r.errb.String())
	}
	r.free = map[string]float64{}
	if code, _ := r.run(t, "--raw", "--resource", "disk"); code != 3 {
		t.Errorf("no readable path: exit %d, want 3", code)
	}
}

func TestCPUBandsAndLoadPerCore(t *testing.T) {
	r := newRig(t)
	r.ncpu = "10\n"
	// per-core division: 55.0 load on 10 cores is 5.5 per core, OK.
	r.load = "{ 55.00 1 1 }\n"
	code, ls := r.run(t, "--raw", "--resource", "cpu")
	if code != 0 || ls[0]["level"] != "OK" || ls[0]["reading"].(map[string]any)["value"].(float64) != 5.5 {
		t.Fatalf("got %d %v", code, ls)
	}
	for _, c := range []struct {
		perCore float64
		want    string
	}{{3.9, "PLENTY"}, {4, "OK"}, {7.9, "OK"}, {8, "TIGHT"}, {13.9, "TIGHT"}, {14, "EXHAUSTED"}} {
		r.load = "{ " + strings.TrimSpace(formatM(c.perCore*10/1024)) + " 1 1 }\n"
		if got := r.level(t, "cpu"); got != c.want {
			t.Errorf("cpu %v per core = %s, want %s", c.perCore, got, c.want)
		}
	}
}

func TestCPUUnreadableOmitted(t *testing.T) {
	r := newRig(t)
	r.load = "garbage"
	if code, _ := r.run(t, "--raw", "--resource", "cpu"); code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
	r.load = "{ 1 1 1 }\n"
	r.ncpu = "0\n"
	if code, _ := r.run(t, "--raw", "--resource", "cpu"); code != 3 {
		t.Errorf("zero cores: exit %d, want 3", code)
	}
}

func TestRAMIsWorstOfSwapAndPressure(t *testing.T) {
	r := newRig(t)
	r.s.Paths = []string{"/"}
	for _, c := range []struct {
		swap  float64
		press string
		want  string
	}{
		{0, "1\n", "PLENTY"},
		{1.99, "1\n", "PLENTY"},
		{2, "1\n", "OK"},
		{3.3, "2\n", "OK"}, // pressure warn is routine, OK
		{9.99, "1\n", "OK"},
		{10, "1\n", "TIGHT"},
		{19.99, "1\n", "TIGHT"},
		{20, "1\n", "EXHAUSTED"},
		{0, "4\n", "EXHAUSTED"},
		{15, "2\n", "TIGHT"},
		{0, "2\n", "OK"},
	} {
		r.swapG, r.press = c.swap, c.press
		if got := r.level(t, "ram"); got != c.want {
			t.Errorf("ram swap %v pressure %q = %s, want %s", c.swap, strings.TrimSpace(c.press), got, c.want)
		}
	}
}

func TestRAMPartialAndMissingReadings(t *testing.T) {
	r := newRig(t)
	r.swapG = 12
	r.press = "garbage"
	if got := r.level(t, "ram"); got != "TIGHT" {
		t.Errorf("swap only = %s", got)
	}
	r2 := newRig(t)
	r2.swapRaw = "nonsense"
	r2.press = "4\n"
	code, ls := r2.run(t, "--raw", "--resource", "ram")
	if code != 0 || ls[0]["level"] != "EXHAUSTED" {
		t.Fatalf("pressure only: %d %v", code, ls)
	}
	if _, has := ls[0]["reading"]; has {
		t.Errorf("pressure-only answer should carry no swap reading: %v", ls[0])
	}
	r3 := newRig(t)
	r3.press = "7\n"
	r3.swapRaw = "nonsense"
	if code, _ := r3.run(t, "--raw", "--resource", "ram"); code != 3 {
		t.Errorf("no readable component must emit no ram answer, exit %d", code)
	}
}

func TestRAMStepDownEngagesAndDropsOneLevel(t *testing.T) {
	r := newRig(t)
	r.swapG, r.free["/"] = 10, 24.9
	if got := r.level(t, "ram"); got != "EXHAUSTED" { // raw TIGHT lowered one step
		t.Errorf("engaged: %s, want EXHAUSTED", got)
	}
}

func TestRAMStepDownNotEngagedAtBoundaries(t *testing.T) {
	r := newRig(t)
	r.swapG, r.free["/"] = 9.9, 10
	if got := r.level(t, "ram"); got != "OK" {
		t.Errorf("swap below 10: %s", got)
	}
	r.swapG, r.free["/"] = 10, 25
	if got := r.level(t, "ram"); got != "TIGHT" {
		t.Errorf("disk exactly 25 is not < 25: %s", got)
	}
}

func TestRAMStepDownNeverBelowExhausted(t *testing.T) {
	r := newRig(t)
	r.swapG, r.free["/"], r.press = 25, 5, "1\n"
	if got := r.level(t, "ram"); got != "EXHAUSTED" {
		t.Errorf("got %s", got)
	}
}

// stateful drives the engaged step-down across invocations (the state file).
func TestRAMStepDownStaysEngagedUntilReleased(t *testing.T) {
	r := newRig(t)
	// Engage: swap 10 with disk 20 (< 25): raw TIGHT, lowered to EXHAUSTED.
	r.swapG, r.free["/"] = 10, 20
	_, ls := r.run(t)
	if got := byRes(ls, "ram")["level"]; got != "EXHAUSTED" {
		t.Fatalf("engaged level = %v", got)
	}
	// Move well past the dwell each time so only the raw/engaged logic limits.
	step := func(swap, free float64) string {
		r.now = r.now.Add(10 * time.Minute)
		r.swapG, r.free["/"] = swap, free
		_, ls := r.run(t)
		return byRes(ls, "ram")["level"].(string)
	}
	// swap 9 GiB, disk 28 GiB: between the release readings; stays engaged.
	// Raw band is OK (9 < 10) lowered to TIGHT; announced rise is one step from EXHAUSTED.
	if got := step(9, 28); got != "TIGHT" {
		t.Errorf("9/28 first step = %s, want TIGHT (one level per dwell, engaged OK->TIGHT)", got)
	}
	if got := step(9, 28); got != "TIGHT" {
		t.Errorf("9/28 second step = %s, want TIGHT (engaged holds OK-1)", got)
	}
	// exactly 8 GiB swap releases.
	if got := step(8, 28); got != "OK" {
		t.Errorf("8/28 = %s, want OK (released)", got)
	}
}

func TestRAMStepDownReleasedByDisk(t *testing.T) {
	r := newRig(t)
	r.swapG, r.free["/"] = 12, 20
	r.run(t)
	r.now = r.now.Add(10 * time.Minute)
	r.swapG, r.free["/"] = 12, 31.24
	_, ls := r.run(t)
	// still engaged: raw TIGHT lowered to EXHAUSTED
	if got := byRes(ls, "ram")["level"]; got != "EXHAUSTED" {
		t.Errorf("31.24 GiB: %v, want EXHAUSTED (still engaged)", got)
	}
	r.now = r.now.Add(10 * time.Minute)
	r.free["/"] = 31.25
	_, ls = r.run(t)
	if got := byRes(ls, "ram")["level"]; got != "TIGHT" {
		t.Errorf("31.25 GiB: %v, want TIGHT (released)", got)
	}
}

func TestRAMRiseNeedsSwapMargin(t *testing.T) {
	r := newRig(t)
	r.swapG, r.free["/"] = 25, 100
	r.run(t) // EXHAUSTED
	// swap 9.9 is in band OK but above TIGHT->OK needs <= 10*0.8 = 8.
	r.now = r.now.Add(10 * time.Minute)
	r.swapG = 18 // TIGHT band, EXHAUSTED->TIGHT needs <= 16
	_, ls := r.run(t)
	if got := byRes(ls, "ram")["level"]; got != "EXHAUSTED" {
		t.Errorf("18 GiB: %v, want EXHAUSTED (margin not cleared)", got)
	}
	r.now = r.now.Add(10 * time.Minute)
	r.swapG = 16
	_, ls = r.run(t)
	if got := byRes(ls, "ram")["level"]; got != "TIGHT" {
		t.Errorf("16 GiB: %v, want TIGHT", got)
	}
}

func TestDiskRiseNeedsMargin(t *testing.T) {
	r := newRig(t)
	r.free["/"] = 5
	r.run(t) // EXHAUSTED
	r.now = r.now.Add(10 * time.Minute)
	r.free["/"] = 14 // TIGHT band, but < 12*1.25=15
	_, ls := r.run(t)
	if got := byRes(ls, "disk")["level"]; got != "EXHAUSTED" {
		t.Errorf("14 GiB: %v", got)
	}
	r.now = r.now.Add(10 * time.Minute)
	r.free["/"] = 15
	_, ls = r.run(t)
	if got := byRes(ls, "disk")["level"]; got != "TIGHT" {
		t.Errorf("15 GiB: %v", got)
	}
}

func TestRunAnswersAllFourAndValidates(t *testing.T) {
	r := newRig(t)
	code, ls := r.run(t)
	if code != 0 || len(ls) != 4 {
		t.Fatalf("exit %d lines %v", code, ls)
	}
	for _, res := range []string{"cpu", "disk", "network", "ram"} {
		l := byRes(ls, res)
		if l == nil || l["owner"] != "host-sensor" {
			t.Fatalf("%s missing: %v", res, ls)
		}
		if _, ok := l["reading"].(map[string]any)["value"].(float64); !ok {
			t.Errorf("%s has no numeric reading: %v", res, l)
		}
	}
}

// SEN-4: the sensor runs only the allowed argv.
func TestOnlyAllowedArgvRuns(t *testing.T) {
	r := newRig(t)
	if code, _ := r.run(t); code != 0 {
		t.Fatal("run failed")
	}
	allowed := map[string]bool{
		"sysctl -n " + SysctlLoad: true, "sysctl -n " + SysctlNCPU: true,
		"sysctl -n " + SysctlSwap: true, "sysctl -n " + SysctlPressure: true,
	}
	calls := r.fake.Calls()
	if len(calls) != 4 {
		t.Errorf("calls = %d, want 4", len(calls))
	}
	for _, c := range calls {
		if k := c.Name + " " + strings.Join(c.Args, " "); !allowed[k] {
			t.Errorf("unexpected argv %q", k)
		}
	}
}

func TestProbeFailureModes(t *testing.T) {
	for name, res := range map[string]cmdrun.Result{
		"start":   {StartErr: errors.New("not found")},
		"ctx":     {CtxErr: context.DeadlineExceeded},
		"nonzero": cmdruntest.Fail(1, "x"),
	} {
		r := newRig(t)
		res := res
		r.sysctlFail = &res
		if code, _ := r.run(t, "--raw", "--resource", "cpu"); code != 3 {
			t.Errorf("%s: exit %d, want 3", name, code)
		}
	}
}

func TestConfigure(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "c.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	r := newRig(t)
	r.free = map[string]float64{"/a": 50, "/b": 7}
	cfg := write("paths = [\"/a\", \"/b\"]\ndisk_plenty_min = 100\ndisk_ok_min = 50.5\ndisk_tight_min = 8\n")
	code, ls := r.run(t, "--raw", "--resource", "disk", "--config", cfg)
	if code != 0 || ls[0]["level"] != "EXHAUSTED" || ls[0]["reading"].(map[string]any)["value"].(float64) != 7 {
		t.Fatalf("custom thresholds/paths: %d %v %s", code, ls, r.errb.String())
	}
	r.free["/b"] = 60
	if _, ls = r.run(t, "--raw", "--resource", "disk", "--config", cfg); ls[0]["level"] != "TIGHT" {
		t.Errorf("50 vs ok_min 50.5: %v", ls)
	}
	for name, body := range map[string]string{
		"unknown key": "bogus = 1\n",
		"wrong type":  "disk_ok_min = \"x\"\n",
		"negative":    "disk_ok_min = -1\n",
		"empty paths": "paths = []\n",
		"paths type":  "paths = 3\n",
		"disk order":  "disk_ok_min = 5\n",
		"cpu order":   "cpu_ok_max = 20\n",
		"swap order":  "swap_ok_max = 1\n",
	} {
		rr := newRig(t)
		if code, _ := rr.run(t, "--config", write(body)); code != 2 {
			t.Errorf("%s: exit %d, want 2 (%s)", name, code, rr.errb.String())
		}
	}
}

func TestBandsOutputFollowsConfig(t *testing.T) {
	r := newRig(t)
	r.out.Reset()
	code := sensorcli.Run([]string{"host-sensor", "--bands"}, sensorcli.Registry{"host-sensor": r.s}, sensorcli.IO{Stdout: &r.out, Stderr: &r.errb})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var infos []levels.Info
	for _, l := range strings.Split(strings.TrimSpace(r.out.String()), "\n") {
		var i levels.Info
		if err := json.Unmarshal([]byte(l), &i); err != nil {
			t.Fatal(err)
		}
		infos = append(infos, i)
	}
	if len(infos) != 4 || infos[1].Resource != "disk" || *infos[1].Bands[1].Min != 12 {
		t.Errorf("bands = %+v", infos)
	}
}

func TestSystemStatfsRoot(t *testing.T) {
	b, err := SystemStatfs("/")
	if err != nil || b == 0 {
		t.Fatalf("SystemStatfs(/) = %d, %v", b, err)
	}
	if _, err := SystemStatfs("/definitely/not/here"); err == nil {
		t.Error("missing path must fail")
	}
}

func TestResourcesAndNew(t *testing.T) {
	s := New()
	if got := strings.Join(s.Resources(), ","); got != "cpu,disk,network,ram" {
		t.Errorf("resources = %s", got)
	}
	if s.Run == nil || s.Statfs == nil || len(s.Paths) != 1 || s.Paths[0] != "/" {
		t.Errorf("New() = %+v", s)
	}
}

func TestConfigureNetwork(t *testing.T) {
	s := New()
	cfg := func(m map[string]any) sensorcli.Config { return sensorcli.Config{Raw: m} }
	if err := s.Configure(cfg(map[string]any{
		"network_targets": []any{"https://one.example/", "https://two.example/"},
		"network_timeout": "3s", "network_failures": int64(2),
	})); err != nil {
		t.Fatal(err)
	}
	if len(s.NetTargets) != 2 || s.Net.Timeout != 3*time.Second || s.NetThreshold != 2 {
		t.Fatalf("%+v", s)
	}
	for _, m := range []map[string]any{
		{"network_targets": []any{}},
		{"network_targets": []any{"http://plain.example"}},
		{"network_targets": "x"},
		{"network_timeout": "0s"},
		{"network_failures": int64(0)},
	} {
		if err := New().Configure(cfg(m)); err == nil {
			t.Errorf("%v must fail", m)
		}
	}
	// A bare struct literal gets the defaults filled in.
	bare := &Sensor{Paths: []string{"/"}, T: Defaults()}
	if err := bare.Configure(cfg(map[string]any{})); err != nil || bare.Net == nil || bare.NetThreshold != 3 || len(bare.NetTargets) != 3 {
		t.Fatalf("%v %+v", err, bare)
	}
}

func TestMeasureNetwork(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(302) }))
	defer srv.Close()
	s := &Sensor{Net: &netprobe.Prober{Client: srv.Client(), Timeout: time.Second}, NetTargets: []string{srv.URL}, NetThreshold: 3}
	var stderr bytes.Buffer
	obs, err := s.Measure(context.Background(), sensorcli.Env{Resource: ResNetwork, Store: levels.NewMemoryStore(), Stderr: &stderr})
	if err != nil || len(obs) != 1 || obs[0].Resource != ResNetwork || obs[0].Level != "AVAILABLE" {
		t.Fatalf("%+v %v", obs, err)
	}
	var found bool
	for _, b := range New().Bands() {
		if b.Resource == ResNetwork && len(b.Bands) == 0 {
			found = true
		}
	}
	if !found {
		t.Error("network must appear in --bands with no bands")
	}
}
