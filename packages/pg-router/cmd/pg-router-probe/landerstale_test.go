package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// Subject: lander-stale files exactly one escalated bead per stale
// pg-git-lander operation, refreshes it on re-run instead of duplicating, and
// writes nothing when no operation is stale. Both subprocesses sit behind the
// runner seam: a fake pg-git-lander answers a canned stopped record (no real
// waiting, no real clock) and a fake pg-connector keeps an in-memory bead
// store, so a second run sees the first run's bead through `issue list`.

const staleStatusJSON = `{"schema":1,"stale_after_s":7200,"stale":[` +
	`{"op_id":"op-1","operation":"land","branch":"drain/x","target":"main","reason":"conflict","stage":"rebase",` +
	`"since":"2026-11-21T01:00:00Z","worktree":"/w/x","log":"/l/op-1.log","age_s":9000,` +
	`"next":["pg-git-lander land --continue op-1","pg-git-lander land --abort op-1"]}]}`

const cleanStatusJSON = `{"schema":1,"stale_after_s":7200,"stale":[]}`

type fakeBead struct {
	ID       string
	Labels   []string
	Metadata map[string]string
	Desc     string
}

type landerWorld struct {
	statusByRepo map[string]string // repo dir -> status stdout
	beads        []*fakeBead
	updates      int
	creates      int
	fake         *cmdruntest.Fake
}

func parseMeta(args []string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--metadata" {
			k, v, _ := strings.Cut(strings.Trim(args[i+1], `"`), "=")
			m[k] = v
		}
	}
	return m
}

func (w *landerWorld) install(t *testing.T) {
	t.Helper()
	f := cmdruntest.New(t)
	f.On(cmdruntest.Any).Respond(func(c cmdrun.Call) cmdrun.Result {
		if c.Name == "pg-git-lander" {
			out, ok := w.statusByRepo[c.Dir]
			if !ok {
				return cmdruntest.Fail(1, "not a repo\n")
			}
			return cmdruntest.Out(out)
		}
		// pg-connector issue <verb> ...
		verb := c.Args[1]
		switch verb {
		case "list":
			var ents []string
			for _, b := range w.beads {
				js, _ := json.Marshal(map[string]any{"id": b.ID, "labels": b.Labels, "metadata": b.Metadata})
				ents = append(ents, string(js))
			}
			return cmdruntest.Out(`{"entities":[` + strings.Join(ents, ",") + `],"present_ids":[],"sources":[]}`)
		case "create":
			w.creates++
			b := &fakeBead{ID: fmt.Sprintf("b-%d", w.creates), Metadata: parseMeta(c.Args)}
			for i, a := range c.Args {
				if a == "--labels" {
					b.Labels = append(b.Labels, c.Args[i+1])
				}
				if a == "--description" {
					b.Desc = c.Args[i+1]
				}
			}
			w.beads = append(w.beads, b)
			return cmdruntest.Out(`{"result":{"id":"` + b.ID + `"}}`)
		case "update":
			w.updates++
			for _, b := range w.beads {
				if b.ID == c.Args[2] {
					for k, v := range parseMeta(c.Args) {
						b.Metadata[k] = v
					}
				}
			}
			return cmdruntest.Out(`{"result":{"id":"` + c.Args[2] + `"}}`)
		}
		return cmdruntest.Fail(99, "unexpected verb "+verb+"\n")
	})
	w.fake = f
	orig := runner
	runner = f.Runner()
	t.Cleanup(func() { runner = orig })
}

func runLander(t *testing.T, repos ...string) (string, error) {
	t.Helper()
	args := []string{"lander-stale"}
	for _, r := range repos {
		args = append(args, "--repo", r)
	}
	var out, errb strings.Builder
	code := run(args, &out, &errb)
	if code != 0 {
		return errb.String(), fmt.Errorf("exit %d", code)
	}
	return errb.String(), nil
}

func TestLanderStaleCreatesOneBeadWithResumeCommands(t *testing.T) {
	w := &landerWorld{statusByRepo: map[string]string{"/repo/a": staleStatusJSON}}
	w.install(t)
	if _, err := runLander(t, "/repo/a"); err != nil {
		t.Fatal(err)
	}
	if len(w.beads) != 1 {
		t.Fatalf("want 1 bead, got %d", len(w.beads))
	}
	b := w.beads[0]
	if b.Metadata[metaFingerprint] != "lander-stale:op-1" {
		t.Fatalf("fingerprint metadata = %q", b.Metadata[metaFingerprint])
	}
	if len(b.Labels) != 1 || b.Labels[0] != "escalated" {
		t.Fatalf("labels = %v", b.Labels)
	}
	for _, want := range []string{"land --continue op-1", "land --abort op-1", "/repo/a", "drain/x"} {
		if !strings.Contains(b.Desc, want) {
			t.Errorf("body lacks %q:\n%s", want, b.Desc)
		}
	}
}

func TestLanderStaleSecondRunRefreshesWithoutDuplicating(t *testing.T) {
	w := &landerWorld{statusByRepo: map[string]string{"/repo/a": staleStatusJSON}}
	w.install(t)
	for i := 0; i < 2; i++ {
		if _, err := runLander(t, "/repo/a"); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.beads) != 1 || w.creates != 1 {
		t.Fatalf("beads=%d creates=%d, want 1/1", len(w.beads), w.creates)
	}
	if w.updates != 1 {
		t.Fatalf("updates=%d, want 1 refresh", w.updates)
	}
	if w.beads[0].Metadata[metaLanderAgeS] != "9000" {
		t.Fatalf("age metadata = %q", w.beads[0].Metadata[metaLanderAgeS])
	}
}

func TestLanderStaleCleanFileNothing(t *testing.T) {
	w := &landerWorld{statusByRepo: map[string]string{"/repo/a": cleanStatusJSON}}
	w.install(t)
	if _, err := runLander(t, "/repo/a"); err != nil {
		t.Fatal(err)
	}
	if w.creates != 0 || w.updates != 0 {
		t.Fatalf("creates=%d updates=%d, want none", w.creates, w.updates)
	}
}

func TestLanderStaleResolvedOperationIsNotRefreshed(t *testing.T) {
	w := &landerWorld{statusByRepo: map[string]string{"/repo/a": staleStatusJSON}}
	w.install(t)
	if _, err := runLander(t, "/repo/a"); err != nil {
		t.Fatal(err)
	}
	w.statusByRepo["/repo/a"] = cleanStatusJSON
	if _, err := runLander(t, "/repo/a"); err != nil {
		t.Fatal(err)
	}
	if w.creates != 1 || w.updates != 0 {
		t.Fatalf("creates=%d updates=%d, want 1/0", w.creates, w.updates)
	}
}

func TestLanderStaleOneFailingRepoIsPartialAndOthersStillFile(t *testing.T) {
	w := &landerWorld{statusByRepo: map[string]string{"/repo/a": staleStatusJSON}}
	w.install(t)
	if _, err := runLander(t, "/repo/missing", "/repo/a"); err == nil || !strings.Contains(err.Error(), "exit 5") {
		t.Fatalf("want exit 5, got %v", err)
	}
	if len(w.beads) != 1 {
		t.Fatalf("beads=%d, want the healthy repo's stale op filed", len(w.beads))
	}
}

func TestLanderStaleAllReposFailingIsTotalFailure(t *testing.T) {
	w := &landerWorld{statusByRepo: map[string]string{}}
	w.install(t)
	if _, err := runLander(t, "/repo/missing"); err == nil || !strings.Contains(err.Error(), "exit 3") {
		t.Fatalf("want exit 3, got %v", err)
	}
}

func TestLanderStaleRequiresRepo(t *testing.T) {
	var out, errb strings.Builder
	if code := run([]string{"lander-stale"}, &out, &errb); code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
}

func TestLanderStaleStatusCallCarriesThresholdAndDir(t *testing.T) {
	w := &landerWorld{statusByRepo: map[string]string{"/repo/a": cleanStatusJSON}}
	w.install(t)
	if _, err := runLander(t, "/repo/a"); err != nil {
		t.Fatal(err)
	}
	calls := w.fake.Calls()
	if len(calls) == 0 {
		t.Fatal("no call recorded")
	}
	c := calls[0]
	if c.Dir != "/repo/a" || fmt.Sprint(c.Args) != "[status --json --stale-after 2h0m0s]" {
		t.Fatalf("call = dir %q args %v", c.Dir, c.Args)
	}
}
