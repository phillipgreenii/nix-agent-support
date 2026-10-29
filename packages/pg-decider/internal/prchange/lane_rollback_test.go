package prchange

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/parity"
)

// The rollback drill of the first apply (docket pg2-6mo45.3, packet P6-a; design
// 6.5, step 8 and risk R11). It runs the REAL backfill driver against the
// tracker the rest of the suite uses (the fake bd in TestLaneFake, a real bd in
// TestLaneE2E): the tracker's before-image is taken, the flip
// marker is written, the real composed command makes every kind of write (an
// update that adopts keyless items, a head refresh, a create, a close and a
// reopen), and then `pg-pr-backfill rollback` lists every write since the marker by
// timestamp from the audit comments and reverts each. The ground truth is the
// tracker's own listing, compared with the before-image.

// driver runs the real pg-pr-backfill against the world's tracker and run record.
type driver struct {
	t   *testing.T
	w   *e2eWorld
	cfg string
}

func newDriver(t *testing.T, w *e2eWorld) *driver {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "pg-pr-backfill.json")
	b, err := json.Marshal(map[string]any{
		"run_record": w.r.runRecordPath(), "beads_dir": w.tr.dir, "backend": "beads", "bd": w.tools.bd,
		"tracker_snapshot": filepath.Join(filepath.Dir(cfg), "tracker-snapshot.json"),
		"set_file":         filepath.Join(filepath.Dir(cfg), "set.json"),
	})
	must(t, err)
	must(t, os.WriteFile(cfg, b, 0o600))
	return &driver{t: t, w: w, cfg: cfg}
}

func (d *driver) run(args ...string) result {
	d.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), contractTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.w.tools.backfill, args...)
	cmd.Env = append(append([]string{}, d.w.tr.env...), "PG_PR_BACKFILL_CONFIG="+d.cfg)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := result{stdout: out.String(), stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.code = ee.ExitCode()
	} else if err != nil {
		d.t.Fatalf("pg-pr-backfill %v: %v", args, err)
	}
	return res
}

func (d *driver) mustRun(args ...string) result {
	d.t.Helper()
	res := d.run(args...)
	if res.code != 0 {
		d.t.Fatalf("pg-pr-backfill %v exited %d:\n%s%s", args, res.code, res.stdout, res.stderr)
	}
	return res
}

// itemFields is the part of an item a rollback restores, as one comparable string.
// bd stores a metadata value that reads as a number as a number, so a value is
// compared by its text.
func itemFields(it map[string]any) string {
	md := map[string]string{}
	for k, v := range metaOf(it) {
		b, _ := json.Marshal(v)
		md[k] = strings.Trim(string(b), `"`)
	}
	b, _ := json.Marshal([]any{it["title"], it["priority"], it["description"], it["labels"], md})
	return string(b)
}

var listingLine = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ) (\S+) (create|update|close|reopen) rule=(\S+)$`)

func rollbackScenarios(l *laneRun) {
	tools, run := l.tools, l.run
	run("a-rollback-drill-lists-every-write-since-the-flip-marker-by-timestamp-and-reverts-each", func(t *testing.T) {
		// One PR with the keyless anchor and review item the old sync left, hydrated.
		w := newE2EWorldWith(t, tools, e2eOpts{n: 1, seed: true})
		id := w.ids[0]
		d := newDriver(t, w)

		// Before the flip: the tracker's before-image, then the flip marker.
		beforeItems := map[string]map[string]any{}
		for _, it := range w.tr.items() {
			beforeItems[str(it["id"])] = it
		}
		d.mustRun("snapshot")
		d.mustRun("flip-marker", "--time", time.Now().UTC().Format(time.RFC3339))

		// A rerun of the snapshot once the flip marker exists must refuse: it would
		// hold the writes the rollback is to undo.
		if res := d.run("snapshot"); res.code != 33 {
			t.Fatalf("a snapshot after the flip marker exited %d, want 33:\n%s%s", res.code, res.stdout, res.stderr)
		}

		// The first apply makes every kind of write. Between the runs the clock
		// moves on (w.tick: a real bd needs it, the fake orders by comment id),
		// so the by-timestamp order is visible.
		w.apply(id) // adoption: the keyless items get their keys (updates)
		w.tick()
		w.setHead(id, sha('b'))
		w.apply(id) // the review item follows the new head (an update)
		review := w.onlyReview()
		w.claim(review.id) // a person takes the review item and finishes it
		w.closeItem(review.id)
		w.tick()
		w.setHead(id, sha('c'))
		w.apply(id) // a new head with no open review item: a create
		w.tick()
		w.setPR(id, func(p *parity.PRFixture) { p.State = "closed" })
		w.apply(id) // the PR closed: the anchor and the open review item close
		w.tick()
		w.setPR(id, func(p *parity.PRFixture) { p.State = "open" })
		w.apply(id) // the PR reopened: the anchor reopens
		afterApply := w.tr.snapshot()

		// The dry run lists and writes nothing.
		dry := d.mustRun("rollback")
		if got := w.tr.snapshot(); sameSnapshot(afterApply, got) != "" {
			t.Fatalf("the dry run wrote to the tracker: %s", sameSnapshot(afterApply, got))
		}
		if !strings.Contains(dry.stdout, "ROLLBACK PLANNED") {
			t.Fatalf("the dry run's last line:\n%s", dry.stdout)
		}
		var times []string
		ops := map[string]int{}
		for _, l := range strings.Split(dry.stdout, "\n") {
			if m := listingLine.FindStringSubmatch(l); m != nil {
				times = append(times, m[1])
				ops[m[3]]++
			}
		}
		for _, op := range []string{"create", "update", "close", "reopen"} {
			if ops[op] == 0 {
				t.Errorf("the by-timestamp listing has no %s write:\n%s", op, dry.stdout)
			}
		}
		if !sort.StringsAreSorted(times) {
			t.Errorf("the listing is not by timestamp: %v", times)
		}
		// The run record's own listing and the tracker's agree on the number of
		// tracker writes (the run record also counts the annotations of the store).
		recorded := 0
		for _, l := range strings.Split(d.mustRun("writes").stdout, "\n") {
			f := strings.Fields(l)
			if len(f) >= 3 && strings.Contains(f[0], "T") && f[2] != "annotate" {
				recorded++
			}
		}
		if recorded != len(times) {
			t.Errorf("the run record lists %d tracker writes since the flip, the audit comments list %d:\n%s", recorded, len(times), dry.stdout)
		}

		// The rollback itself.
		res := d.mustRun("rollback", "--apply")
		if !strings.Contains(res.stdout, "ROLLBACK DONE") {
			t.Fatalf("the rollback's output:\n%s%s", res.stdout, res.stderr)
		}
		now := map[string]map[string]any{}
		for _, it := range w.tr.items() {
			now[str(it["id"])] = it
		}
		for bid, b := range beforeItems {
			c, ok := now[bid]
			if !ok {
				t.Errorf("item %s is gone", bid)
				continue
			}
			if itemFields(b) != itemFields(c) {
				t.Errorf("item %s was not put back:\n before %s\n after  %s", bid, itemFields(b), itemFields(c))
			}
			// A person's close of the review item stands; everything else is as before.
			want := str(b["status"])
			if bid == review.id {
				want = "closed"
			}
			if str(c["status"]) != want {
				t.Errorf("item %s is %q, want %q", bid, str(c["status"]), want)
			}
		}
		created := 0
		for cid, c := range now {
			if _, was := beforeItems[cid]; was {
				continue
			}
			created++
			if str(c["status"]) != "closed" {
				t.Errorf("the item %s the lane created is %q, want closed", cid, str(c["status"]))
			}
		}
		if created == 0 {
			t.Error("the drill created nothing, so the revert of a create was not shown")
		}

		// A second rollback has nothing left to do and writes nothing.
		settled := w.tr.snapshot()
		again := d.mustRun("rollback", "--apply")
		if !strings.Contains(again.stdout, "writes total=0") || !strings.Contains(again.stdout, "ROLLBACK DONE reverted=0") {
			t.Errorf("the second rollback:\n%s", again.stdout)
		}
		if got := w.tr.snapshot(); sameSnapshot(settled, got) != "" {
			t.Errorf("the second rollback wrote: %s", sameSnapshot(settled, got))
		}
	})
}
