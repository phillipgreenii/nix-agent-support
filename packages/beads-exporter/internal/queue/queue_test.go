package queue

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
)

// realQueuesPath is the committed queue mirror, read through the repo root so
// the tests exercise the real file rather than a copy.
const realQueuesPath = "../../../../claude-marketplace/pb/queues.json"

func bead(id, typ string, labels ...string) bd.Bead {
	return bd.Bead{ID: id, IssueType: typ, Labels: labels, Status: "open"}
}

func TestClassification(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    Class
		wantErr string
	}{
		{"empty list is client-side", nil, ClientSide, ""},
		{"label", []string{"--label", "a"}, ClientSide, ""},
		{"label equals form", []string{"--label=a"}, ClientSide, ""},
		{"exclude-label and exclude-type", []string{"--exclude-label", "a,b", "--exclude-type", "epic"}, ClientSide, ""},
		{"priority spawns", []string{"--priority", "1"}, SpawnOnly, ""},
		{"parent spawns", []string{"--parent", "alpha-1"}, SpawnOnly, ""},
		{"parent equals form", []string{"--parent=alpha-1"}, SpawnOnly, ""},
		{"boolean spawn flag", []string{"--unassigned"}, SpawnOnly, ""},
		{"mixed client and spawn", []string{"--label", "a", "--priority", "2"}, SpawnOnly, ""},
		{"claim rejected", []string{"--claim"}, "", "not an allowed read-only"},
		{"include-deferred rejected", []string{"--include-deferred"}, "", "not an allowed read-only"},
		{"unknown flag rejected", []string{"--frobnicate", "x"}, "", "not an allowed read-only"},
		{"short flag rejected", []string{"-p", "1"}, "", "only long flags"},
		{"positional rejected", []string{"alpha-1"}, "", "only long flags"},
		{"missing client value", []string{"--label"}, "", "requires a value"},
		{"missing spawn value", []string{"--priority"}, "", "requires a value"},
		{"empty client value", []string{"--exclude-label", " , "}, "", "empty value"},
		{"boolean with value", []string{"--unassigned=true"}, "", "takes no value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := New("q", tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if q.Class != tc.want {
				t.Fatalf("class = %q, want %q", q.Class, tc.want)
			}
			if (q.Filter() != nil) != (tc.want == ClientSide) {
				t.Fatalf("Filter() presence = %v for class %q", q.Filter() != nil, q.Class)
			}
			if len(q.Args) != len(tc.args) || (len(tc.args) > 0 && !reflect.DeepEqual(q.Args, tc.args)) {
				t.Fatalf("Args = %v, want %v", q.Args, tc.args)
			}
		})
	}
}

func TestFilterSemantics(t *testing.T) {
	mk := func(args ...string) *Filter {
		q, err := New("q", args)
		if err != nil {
			t.Fatal(err)
		}
		return q.Filter()
	}
	tmpl := bead("t", "task")
	tmpl.IsTemplate = true
	cases := []struct {
		name string
		f    *Filter
		b    bd.Bead
		want bool
	}{
		{"label has", mk("--label", "a"), bead("1", "task", "a"), true},
		{"label missing", mk("--label", "a"), bead("1", "task", "b"), false},
		{"label no labels", mk("--label", "a"), bead("1", "task"), false},
		{"multi label comma is AND", mk("--label", "a,b"), bead("1", "task", "a"), false},
		{"multi label comma all present", mk("--label", "a,b"), bead("1", "task", "b", "a"), true},
		{"repeated label is AND", mk("--label", "a", "--label", "b"), bead("1", "task", "a"), false},
		{"repeated label all present", mk("--label", "a", "--label", "b"), bead("1", "task", "a", "b", "c"), true},
		{"exclude-label comma is ANY", mk("--exclude-label", "a,b"), bead("1", "task", "b"), false},
		{"exclude-label none present", mk("--exclude-label", "a,b"), bead("1", "task", "c"), true},
		{"exclude-label repeated", mk("--exclude-label", "a", "--exclude-label", "b"), bead("1", "task", "b"), false},
		{"exclude-type hit", mk("--exclude-type", "epic"), bead("1", "epic"), false},
		{"exclude-type comma", mk("--exclude-type", "epic,handoff"), bead("1", "handoff"), false},
		{"exclude-type miss", mk("--exclude-type", "epic"), bead("1", "task"), true},
		{"whitespace trimmed", mk("--label", " a , b "), bead("1", "task", "a", "b"), true},
		{"template dropped", mk(), tmpl, false},
		{"empty filter passes", mk(), bead("1", "task"), true},
		{"combined", mk("--label", "h", "--exclude-label", "x", "--exclude-type", "handoff"), bead("1", "task", "h"), true},
		{"combined excluded", mk("--label", "h", "--exclude-label", "x", "--exclude-type", "handoff"), bead("1", "task", "h", "x"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Match(tc.b); got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyKeepsOrderAndDropsTemplates(t *testing.T) {
	q, _ := New("q", []string{"--exclude-type", "epic"})
	tmpl := bead("t", "task")
	tmpl.IsTemplate = true
	in := []bd.Bead{bead("1", "task"), bead("2", "epic"), tmpl, bead("3", "bug")}
	got := q.Filter().Apply(in)
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "3" {
		t.Fatalf("Apply = %+v", got)
	}
	got = DropTemplates(in)
	if len(got) != 3 || got[0].ID != "1" || got[1].ID != "2" || got[2].ID != "3" {
		t.Fatalf("DropTemplates = %+v", got)
	}
}

type queueFile []struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
}

// TestRealQueuesFileClassifiesClientSide pins the committed mirror: exactly the
// four expected queues, all computable in process.
func TestRealQueuesFileClassifiesClientSide(t *testing.T) {
	raw, err := os.ReadFile(realQueuesPath)
	if err != nil {
		t.Fatalf("read real queues file: %v", err)
	}
	var qs queueFile
	if err := json.Unmarshal(raw, &qs); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, spec := range qs {
		names = append(names, spec.Name)
		q, err := New(spec.Name, spec.Args)
		if err != nil {
			t.Fatalf("queue %s: %v", spec.Name, err)
		}
		if q.Class != ClientSide {
			t.Fatalf("queue %s class = %q, want client-side", spec.Name, q.Class)
		}
	}
	want := []string{"drain-claim", "drain-termination", "unblock-human", "unblock-human-focus"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("queue names = %v, want %v", names, want)
	}
}

// TestRealQueuesFilterBehaviour runs the real queue definitions over a fixed
// population and checks each queue's membership.
func TestRealQueuesFilterBehaviour(t *testing.T) {
	raw, err := os.ReadFile(realQueuesPath)
	if err != nil {
		t.Fatal(err)
	}
	var qs queueFile
	if err := json.Unmarshal(raw, &qs); err != nil {
		t.Fatal(err)
	}
	pop := []bd.Bead{
		bead("plain", "task"),
		bead("epic", "epic"),
		bead("human", "task", "human"),
		bead("humanfocus", "task", "human-focus-required"),
		bead("campaign", "task", "refactor-campaign"),
		bead("handoff-human", "handoff", "human"),
		bead("human-campaign", "task", "human", "refactor-campaign"),
	}
	want := map[string][]string{
		"drain-claim":         {"plain"},
		"drain-termination":   {"plain", "epic"},
		"unblock-human":       {"human"},
		"unblock-human-focus": {"humanfocus"},
	}
	for _, spec := range qs {
		q, err := New(spec.Name, spec.Args)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, b := range q.Filter().Apply(pop) {
			got = append(got, b.ID)
		}
		if !reflect.DeepEqual(got, want[spec.Name]) {
			t.Fatalf("queue %s members = %v, want %v", spec.Name, got, want[spec.Name])
		}
	}
}

// TestRecordedFlagsMatchWhatIsPassed keeps testdata/bd/flags.txt exactly equal
// to the flags the exporter can pass to bd: the argv builders plus the queue
// allowlist. A flag added in code without being recorded (so never checked
// against a bd package) fails here, and so does a stale recorded flag.
func TestRecordedFlagsMatchWhatIsPassed(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/bd/flags.txt")
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		recorded[line] = true
	}

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	argvs := map[string][]string{
		"list":     bd.ListArgv(bd.ListOpts{All: true, CreatedAfter: since, ClosedAfter: since}),
		"ready":    bd.ReadyArgv(nil),
		"blocked":  bd.BlockedArgv(),
		"count":    bd.CountByStatusArgv(),
		"statuses": bd.StatusesArgv(),
	}
	passed := map[string]bool{}
	perSub := map[string]map[string]bool{}
	for sub, argv := range argvs {
		perSub[sub] = map[string]bool{}
		for _, tok := range argv[1:] {
			if strings.HasPrefix(tok, "-") {
				perSub[sub][tok] = true
			}
		}
	}
	// A flag every subcommand carries is recorded once under "*".
	for flag := range perSub["list"] {
		everywhere := true
		for _, flags := range perSub {
			everywhere = everywhere && flags[flag]
		}
		if everywhere {
			passed["* "+flag] = true
		}
	}
	for sub, flags := range perSub {
		for flag := range flags {
			if !passed["* "+flag] {
				passed[sub+" "+flag] = true
			}
		}
	}
	for _, flag := range Flags() {
		passed["ready "+flag] = true
	}

	for k := range passed {
		if !recorded[k] {
			t.Errorf("flag %q can be passed to bd but is not recorded in testdata/bd/flags.txt", k)
		}
	}
	for k := range recorded {
		if !passed[k] {
			t.Errorf("flags.txt records %q but nothing passes it", k)
		}
	}
}
