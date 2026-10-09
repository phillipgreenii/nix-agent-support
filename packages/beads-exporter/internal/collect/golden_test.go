package collect

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
	"github.com/phillipgreenii/beads-exporter/internal/queue"
)

var update = flag.Bool("update", false, "rewrite the golden exposition files and metrics.txt")

const (
	testdataDir    = "../../testdata"
	metricsTxtPath = "../../metrics.txt"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func bead(id, status, typ string, prio int, created string, labels ...string) bd.Bead {
	b := mkBead(id, status, typ, prio, at(created), labels...)
	return b
}

// stepClock advances the clock 100ms on every adapter call so durations are
// deterministic.
func stepClock(a *fakeAdapter, c *fakeClock) {
	a.onCall = func(string) error { c.Advance(100 * time.Millisecond); return nil }
}

func goldenSuccess(t *testing.T) string {
	t.Helper()
	clock := newClock()

	started := at("2026-05-25T10:00:00Z")
	inProgress := bead("alpha-2", "in_progress", "task", 2, "2026-05-20T08:00:00Z", "area-a")
	inProgress.StartedAt = &started
	inProgress.Assignee = strandedSession + "-drain"
	deferred := bead("alpha-3", "open", "bug", 0, "2026-05-28T08:00:00Z")
	until := at("2026-07-01T00:00:00Z")
	deferred.DeferUntil = &until
	ready1 := bead("alpha-1", "open", "task", 1, "2026-05-30T08:00:00Z", "area-a", "human")
	ready1.Assignee = "drain-" + liveSession
	claimedOpen := bead("alpha-4", "open", "task", 2, "2026-05-29T08:00:00Z")
	claimedOpen.Assignee = "worker-gamma"
	claimedOpen.UpdatedAt = ptr(at("2026-05-29T09:00:00Z"))
	ready2 := bead("alpha-8", "open", "epic", 2, "2026-05-31T08:00:00Z", "human-focus-required")
	ready3 := bead("alpha-9", "open", "task", 3, "2026-05-31T09:00:00Z", "odd\"label\\path", "multi\nline", "caf\u00e9", "bad\xffbyte", "__other__")
	alphaList := []bd.Bead{
		ready1, inProgress, deferred,
		claimedOpen,
		bead("alpha-5", "open", "merge-request", 2, "2026-05-10T08:00:00Z"),
		bead("alpha-6", "pinned", "task", 3, "2026-04-01T08:00:00Z"),
		bead("alpha-7", "review", "epic", 2, "2026-03-01T08:00:00Z"),
		ready2, ready3,
	}
	alpha := &fakeAdapter{
		list:       alphaList,
		ready:      []bd.Bead{ready1, ready2, ready3},
		blocked:    []bd.Bead{claimedOpen},
		counts:     map[string]int{"closed": 7, "open": 99},
		statuses:   []string{"open", "in_progress", "blocked", "deferred", "closed", "pinned", "hooked", "review", "archived"},
		spawnReady: map[string][]bd.Bead{"--priority 1": {ready1}},
		created:    []bd.Bead{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}},
		closed:     []bd.Bead{{ID: "e"}, {ID: "f"}},
	}
	stepClock(alpha, clock)

	beta := &fakeAdapter{
		counts:   map[string]int{},
		statuses: []string{"open", "in_progress", "blocked", "deferred", "closed", "pinned", "hooked"},
	}
	stepClock(beta, clock)

	queues := []queue.Queue{
		mustQueue(t, "drain-claim", "--exclude-label", "human,human-focus-required", "--exclude-type", "epic"),
		mustQueue(t, "unblock-human", "--label", "human"),
		mustQueue(t, "by-priority", "--priority", "1"),
	}
	dbs := []DB{{Name: "alpha", Adapter: alpha}, {Name: "beta", Adapter: beta}}
	mp := NewMainPass(queues, 7)
	sp := NewStrandedPass(StrandedConfig{ClaudeDir: strandedClaudeDir(t, clock.Now()), Window: 6 * time.Hour}, quietLogger())
	c := New(clock, dbs, []Pass{mp, ThroughputPass{}, sp}, quietLogger())
	mp.Init(context.Background(), dbs)
	c.RunPass(context.Background(), PassMain)
	c.RunPass(context.Background(), PassThroughput)
	c.RunPass(context.Background(), PassStranded)
	return renderSnap(t, c.Snapshot())
}

func goldenFailure(t *testing.T) string {
	t.Helper()
	clock := newClock()
	claimed := bead("alpha-1", "open", "task", 1, "2026-05-30T08:00:00Z")
	claimed.Assignee = "worker-gamma"
	alpha := &fakeAdapter{
		list:     []bd.Bead{claimed},
		ready:    []bd.Bead{claimed},
		counts:   map[string]int{"closed": 1},
		statuses: []string{"open", "closed"},
		created:  []bd.Bead{{ID: "a"}},
		closed:   nil,
	}
	stepClock(alpha, clock)
	beta := &fakeAdapter{
		counts:   map[string]int{},
		statuses: []string{"open", "closed"},
		fail: map[string]error{
			"list":     failure.New(failure.StaleIssuesJSONL, "guard", errf("issues.jsonl exists")),
			"list-all": failure.New(failure.SchemaSkew, "list", errf("bare array")),
		},
	}
	stepClock(beta, clock)

	dbs := []DB{{Name: "alpha", Adapter: alpha}, {Name: "beta", Adapter: beta}}
	mp := NewMainPass([]queue.Queue{mustQueue(t, "drain-claim", "--exclude-type", "epic")}, 3)
	claudeDir := strandedClaudeDir(t, clock.Now())
	sp := NewStrandedPass(StrandedConfig{ClaudeDir: claudeDir, Window: 6 * time.Hour}, quietLogger())
	c := New(clock, dbs, []Pass{mp, ThroughputPass{}, sp}, quietLogger())
	mp.Init(context.Background(), dbs)

	// Cycle 1: alpha succeeds on all three passes.
	c.RunPass(context.Background(), PassMain)
	c.RunPass(context.Background(), PassThroughput)
	c.RunPass(context.Background(), PassStranded)
	// Cycle 2: alpha's main pass times out (its series are dropped and the
	// last-success timestamp stays frozen at cycle 1); its throughput pass
	// still succeeds. beta has failed every pass from the start. The stranded
	// pass loses its transcript tree, so it fails with transcript_error and
	// drops its series while its last-success timestamp stays frozen.
	clock.Advance(time.Minute)
	if err := os.RemoveAll(filepath.Join(claudeDir, "projects")); err != nil {
		t.Fatal(err)
	}
	alpha.mu.Lock()
	alpha.fail = map[string]error{"blocked": failure.New(failure.Timeout, "blocked", errf("deadline"))}
	alpha.mu.Unlock()
	c.RunPass(context.Background(), PassMain)
	alpha.mu.Lock()
	alpha.fail = nil
	alpha.mu.Unlock()
	c.RunPass(context.Background(), PassThroughput)
	c.RunPass(context.Background(), PassStranded)
	return renderSnap(t, c.Snapshot())
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(testdataDir, name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(want) != got {
		t.Fatalf("golden %s differs (rerun with -update to accept).\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldenSuccessSnapshot(t *testing.T) {
	got := goldenSuccess(t)
	checkGolden(t, "success.prom", got)
	// The escaping cases of the fixture must have survived to the output.
	for _, want := range []string{
		`bead_label="odd\"label\\path"`,
		`bead_label="multi\nline"`,
		"bead_label=\"caf\u00e9\"",
		"bead_label=\"bad\uFFFDbyte\"",
		`bead_label="__other__"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("golden lacks %s:\n%s", want, got)
		}
	}
}

func TestGoldenFailureSnapshot(t *testing.T) {
	checkGolden(t, "failure.prom", goldenFailure(t))
}

var (
	helpLine = regexp.MustCompile(`(?m)^# HELP (\S+) `)
	typeLine = regexp.MustCompile(`(?m)^# TYPE (\S+) `)
)

// helpFamilies returns the sorted union of HELP family names across every
// golden exposition file.
func helpFamilies(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(testdataDir, "*.prom"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no goldens found: %v", err)
	}
	seen := map[string]struct{}{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range helpLine.FindAllStringSubmatch(string(raw), -1) {
			seen[m[1]] = struct{}{}
		}
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func TestMetricsTxtIsTheUnionOfHelpFamiliesAcrossGoldens(t *testing.T) {
	// Regenerate the goldens first so the union reflects them under -update.
	if *update {
		checkGolden(t, "success.prom", goldenSuccess(t))
		checkGolden(t, "failure.prom", goldenFailure(t))
		if err := os.WriteFile(metricsTxtPath, []byte(strings.Join(helpFamilies(t), "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(metricsTxtPath)
	if err != nil {
		t.Fatalf("read metrics.txt: %v", err)
	}
	want := strings.Join(helpFamilies(t), "\n") + "\n"
	if string(raw) != want {
		t.Fatalf("metrics.txt is not the union of HELP families across goldens.\ngot:\n%s\nwant:\n%s", raw, want)
	}
}

func TestGoldensCoverEveryRegisteredFamily(t *testing.T) {
	var registered []string
	for _, f := range metrics.Default().Families() {
		registered = append(registered, f.Name)
	}
	sort.Strings(registered)
	got := helpFamilies(t)
	if strings.Join(got, ",") != strings.Join(registered, ",") {
		t.Fatalf("goldens cover %v\nregistry has %v", got, registered)
	}
}

func TestGoldensHaveOneHelpAndTypePerFamilyAndNoBuildInfo(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testdataDir, "*.prom"))
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		for _, re := range []*regexp.Regexp{helpLine, typeLine} {
			seen := map[string]int{}
			for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
				seen[m[1]]++
				if seen[m[1]] > 1 {
					t.Fatalf("%s: %s appears more than once", f, m[1])
				}
			}
		}
		if bytes.Contains(raw, []byte("build_info")) {
			t.Fatalf("%s mentions build_info", f)
		}
	}
}

// TestGoldensLabelAllowlist checks every series line of every golden against
// the family's allowed label names.
func TestGoldensLabelAllowlist(t *testing.T) {
	allowed := map[string][]string{}
	for _, f := range metrics.Default().Families() {
		allowed[f.Name] = f.Labels
	}
	series := regexp.MustCompile(`^([a-z0-9_]+)\{(.*)\} \S+$`)
	labelName := regexp.MustCompile(`([a-z_]+)="`)
	files, _ := filepath.Glob(filepath.Join(testdataDir, "*.prom"))
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(raw), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			m := series.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("%s: unparseable series line %q", f, line)
			}
			want, ok := allowed[m[1]]
			if !ok {
				t.Fatalf("%s: series of unknown family %s", f, m[1])
			}
			// Strip quoted values so label-name matching cannot be fooled by them.
			stripped := regexp.MustCompile(`"(?:[^"\\]|\\.)*"`).ReplaceAllString(m[2], `""`)
			var got []string
			for _, ln := range labelName.FindAllStringSubmatch(strings.ReplaceAll(stripped, `""`, `"`), -1) {
				got = append(got, ln[1])
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%s: %s has labels %v, allowlist is %v", f, m[1], got, want)
			}
		}
	}
}

func TestGoldenZeroFillAndOmission(t *testing.T) {
	got := goldenSuccess(t)
	// Zero-filled fixed sets: all six states and the stored statuses for the
	// empty database.
	for _, s := range []string{"in_progress", "deferred", "blocked", "ready", "tracking", "other"} {
		if !strings.Contains(got, `beads_issues{db="beta",state="`+s+`"} 0`) {
			t.Fatalf("beta state %s not zero-filled", s)
		}
	}
	for _, s := range []string{"open", "closed", "review", "archived"} {
		if !strings.Contains(got, `beads_issues_stored{db="alpha",status="`+s+`"}`) {
			t.Fatalf("alpha stored status %s missing", s)
		}
	}
	// Timestamps are omitted for an empty set.
	if strings.Contains(got, `beads_oldest_timestamp_seconds{db="beta"`) || strings.Contains(got, `beads_queue_oldest_timestamp_seconds{db="beta"`) {
		t.Fatal("timestamp series emitted for an empty set")
	}
	if strings.Contains(got, `beads_oldest_timestamp_seconds{db="alpha",state="blocked"`) == false {
		t.Fatal("populated state lacks its oldest timestamp")
	}
}
