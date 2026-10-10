//go:build contract

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/unstick"
)

// pbBin builds the real pb binary once per test into a temp dir.
func pbBin(t *testing.T) string {
	t.Helper()
	skipNoBin(t, "go")
	bin := filepath.Join(t.TempDir(), "pb")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = hermeticEnviron()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build pb: %v\n%s", err, out)
	}
	return bin
}

// runPB runs the pb binary and returns stdout, stderr and the exit code.
func runPB(t *testing.T, bin string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Env = hermeticEnviron()
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run pb %v: %v", args, err)
	}
	return stdout.String(), stderr.String(), code
}

func mustPB(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, errOut, code := runPB(t, bin, args...)
	if code != 0 {
		t.Fatalf("pb %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

// seedBead creates a bead through real bd and returns its id.
func seedBead(t *testing.T, ws, title string, extra ...string) string {
	t.Helper()
	args := append([]string{"create", title, "--json"}, extra...)
	return dataID(t, shellOut(t, ws, "bd", args...))
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

func readIDList(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ids = append(ids, l)
		}
	}
	return ids
}

// TestContract_UnstickSweep drives pb unstick end to end against a REAL bd in a
// throwaway embedded Dolt database (never a server, never the real tracker):
// shape facts over real export/ready output, prepare, marker + close, report.
func TestContract_UnstickSweep(t *testing.T) {
	skipNoBin(t, "bd")
	bin := pbBin(t) // before isolate(): the Go caches must not land in the temp HOME
	isolate(t)
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BD_NON_INTERACTIVE", "1")

	ws := t.TempDir()
	bdInit(t, ws, "ct")
	meta, err := os.ReadFile(filepath.Join(ws, ".beads", "metadata.json"))
	if err != nil || !strings.Contains(string(meta), `"dolt_mode": "embedded"`) {
		t.Skipf("throwaway bd database is not embedded; refusing to run (%v): %s", err, meta)
	}

	// --- seed a tiny synthetic workspace -------------------------------
	drain := seedBead(t, ws, "drainable ready bead")
	headA := seedBead(t, ws, "chain head")
	tailB := seedBead(t, ws, "chain tail")
	shellOut(t, ws, "bd", "dep", "add", tailB, headA) // tailB depends on headA
	human := seedBead(t, ws, "human park", "-l", "human")
	valid := seedBead(t, ws, "bead with valid marker")
	shellOut(t, ws, "bd", "dep", "add", valid, human)
	malformed := seedBead(t, ws, "bead with date-only marker")
	shellOut(t, ws, "bd", "dep", "add", malformed, human)
	elapsed := seedBead(t, ws, "deferred, date elapsed")
	// bd warns about a past date but stores it.
	shellOut(t, ws, "bd", "update", elapsed, "--status", "deferred", "--defer", "2026-01-01")
	future := seedBead(t, ws, "deferred, date in the future")
	shellOut(t, ws, "bd", "update", future, "--status", "deferred", "--defer", "2099-01-01")
	shellOut(t, ws, "bd", "comments", "add", drain, "a synthetic comment")

	// A recent, well-formed marker made by `pb unstick marker`, applied the way
	// a worker does (bd update --append-notes). Written last, so nothing it
	// depends on is newer than the marker.
	markerTS := time.Now().UTC().Truncate(time.Second)
	validLine := strings.TrimSpace(mustPB(t, bin, "unstick", "marker", "--outcome", "unchanged",
		"--reason", "waits on a human decision", "--recheck-when", "on-change",
		"--now", markerTS.Format(time.RFC3339)))
	if _, err := unstick.ParseMarker(validLine); err != nil {
		t.Fatalf("pb marker output does not round-trip: %v: %s", err, validLine)
	}
	shellOut(t, ws, "bd", "update", valid, "--append-notes", validLine)
	shellOut(t, ws, "bd", "update", malformed, "--append-notes",
		"[unstick 2026-10-10] unchanged: waits on a human decision; recheck-when: on-change")

	now := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	nowArg := now.Format(time.RFC3339)

	// --- (2) prepare end to end (then shape facts over its REAL files) ----------------------------------------
	workdir := filepath.Join(t.TempDir(), "sweep")
	out := mustPB(t, bin, "unstick", "prepare", "--root", ws, "--workdir", workdir, "--now", nowArg, "--json")
	var sum prepareSummary
	if err := json.Unmarshal([]byte(out), &sum); err != nil {
		t.Fatalf("prepare json: %v: %s", err, out)
	}
	if sum.Workdir != workdir || sum.Start != nowArg {
		t.Errorf("workdir/start = %s / %s", sum.Workdir, sum.Start)
	}
	// Targets: tailB (live), valid (marker), malformed + future (review) and
	// the deferred-elapsed bead. Real bd lists a deferred bead whose date has
	// elapsed in `bd ready`, so triage may file it as live (drainable seed) or
	// review; this test only requires that it lands in exactly one class.
	classOf := func(id string) string {
		var in []string
		for class, file := range map[string]string{"live": "triage-live.txt", "marker": "triage-marker.txt", "review": "triage-review.txt"} {
			if slices.Contains(readIDList(t, filepath.Join(workdir, file)), id) {
				in = append(in, class)
			}
		}
		if len(in) != 1 {
			t.Errorf("%s is in classes %v, want exactly one", id, in)
			return ""
		}
		return in[0]
	}
	elapsedClass := classOf(elapsed)
	t.Logf("deferred-elapsed bead %s classified %q", elapsed, elapsedClass)
	wantLive := []string{tailB}
	wantReview := []string{malformed, future}
	switch elapsedClass {
	case "live":
		wantLive = append(wantLive, elapsed)
	case "review":
		wantReview = append(wantReview, elapsed)
	}
	if want := "targets 5 = live " + strconv.Itoa(len(wantLive)) + " + marker 1 + review " + strconv.Itoa(len(wantReview)); sum.Partition != want {
		t.Errorf("partition = %q, want %q", sum.Partition, want)
	}
	c := sum.Counts
	if c.Targets != 5 || c.LiveSkip != len(wantLive) || c.MarkerSkip != 1 || c.Review != len(wantReview) {
		t.Errorf("counts = %+v", c)
	}
	if c.Open != 6 || c.Blocked != 0 || c.Deferred != 2 || c.InProgress != 0 {
		t.Errorf("status counts = %+v (6 open, 2 deferred expected)", c)
	}
	if c.Ready < 3 || c.Drainable < 2 {
		t.Errorf("ready/drainable = %d/%d, want >= 3/2", c.Ready, c.Drainable)
	}
	if sum.MalformedMarkers.Count != 1 || !slices.Equal(sum.MalformedMarkers.IDs, []string{malformed}) {
		t.Errorf("malformed markers = %+v, want [%s]", sum.MalformedMarkers, malformed)
	}
	switch sum.GateCheck {
	case "ok", "partial", "unavailable":
	default:
		t.Errorf("gate_check = %q", sum.GateCheck)
	}
	for _, f := range []string{
		"export.jsonl", "ready.json", "prepare.json", "progress.txt", "followups.txt",
		"triage-targets.txt", "triage-live.txt", "triage-marker.txt", "triage-review.txt", "triage-drain.txt",
	} {
		if _, err := os.Stat(filepath.Join(workdir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	for dir, want := range map[string][]string{
		"triage-targets.txt": {tailB, valid, malformed, future, elapsed},
		"triage-live.txt":    wantLive,
		"triage-marker.txt":  {valid},
		"triage-review.txt":  wantReview,
	} {
		if got := sortedCopy(readIDList(t, filepath.Join(workdir, dir))); !slices.Equal(got, sortedCopy(want)) {
			t.Errorf("%s = %v, want %v", dir, got, want)
		}
	}
	drainIDs := readIDList(t, filepath.Join(workdir, "triage-drain.txt"))
	if !slices.Contains(drainIDs, drain) || !slices.Contains(drainIDs, headA) || slices.Contains(drainIDs, human) {
		t.Errorf("drainable = %v: want %s and %s, never the human bead %s", drainIDs, drain, headA, human)
	}
	// Batch coverage: every REVIEW id in exactly one batch.
	if len(sum.Batches) != 1 || sum.Batches[0].Size != len(wantReview) {
		t.Fatalf("batches = %+v, want one batch of %d", sum.Batches, len(wantReview))
	}
	batch := readIDList(t, filepath.Join(workdir, "batches", sum.Batches[0].Name))
	if !slices.Equal(sortedCopy(batch), sortedCopy(wantReview)) {
		t.Errorf("batch = %v", batch)
	}
	if _, err := os.Stat(filepath.Join(workdir, "facts", sum.Batches[0].Name+".json")); err != nil {
		t.Errorf("missing facts file: %v", err)
	}
	// A second prepare must not reuse the directory.
	if _, _, code := runPB(t, bin, "unstick", "prepare", "--root", ws, "--workdir", workdir, "--now", nowArg); code == 0 {
		t.Errorf("prepare into an existing workdir must fail")
	}

	// --- (1) real export / ready shape facts ---------------------------
	// Taken from the files prepare wrote (REAL bd output). Note `bd ready`
	// un-defers elapsed deferred beads, which is why prepare must export first
	// and why this test makes no bd ready call before prepare.
	exportPath := filepath.Join(workdir, "export.jsonl")
	rawExport, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	rawReady, err := os.ReadFile(filepath.Join(workdir, "ready.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertRealShape(t, rawExport, rawReady, realShape{child: tailB, parent: headA, commented: drain})
	if !strings.HasPrefix(strings.TrimSpace(string(rawReady)), "{") {
		t.Errorf("bd ready --json with BD_JSON_ENVELOPE=1 should be an envelope object: %.80s", rawReady)
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rawReady, &env); err != nil {
		t.Fatal(err)
	}
	if _, err := unstick.ParseReady(env.Data); err != nil {
		t.Errorf("ParseReady must tolerate a bare array (the clean-env shape): %v", err)
	}
	// The elapsed deferred bead was exported as deferred, with a Z-form defer_until.
	rows, err := unstick.ReadExportFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == elapsed && (r.Status != unstick.StatusDeferred || r.DeferUntil == "") {
			t.Errorf("elapsed bead exported as %+v, want status deferred with defer_until", r)
		}
	}
	// The malformed-marker finder sees the date-only marker, not the valid one.
	chk, _, code := runPB(t, bin, "unstick", "marker", "--check", "--export", exportPath, "--json")
	if code == 0 {
		t.Errorf("marker --check --export should exit non-zero (date-only marker): %s", chk)
	}
	var chkRes markerCheckResult
	if err := json.Unmarshal([]byte(chk), &chkRes); err != nil {
		t.Fatalf("marker --check json: %v: %s", err, chk)
	}
	if len(chkRes.Beads) != 1 || chkRes.Beads[0].ID != malformed {
		t.Errorf("malformed_beads = %+v, want only %s", chkRes.Beads, malformed)
	}

	// --- (3) worker actions, then report -------------------------------
	reportNow := now.Add(5 * time.Minute)
	workerLine := strings.TrimSpace(mustPB(t, bin, "unstick", "marker", "--outcome", "retargeted",
		"--reason", "now waits on the chain head", "--recheck-when", headA+" closes",
		"--now", now.Add(time.Minute).Format(time.RFC3339)))
	shellOut(t, ws, "bd", "update", malformed, "--append-notes", workerLine)
	shellOut(t, ws, "bd", "close", future, "-r", "stale: nothing left to wait for")
	if err := os.WriteFile(filepath.Join(workdir, "results", "B01.md"),
		[]byte("closed "+future+": stale: nothing left to wait for\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A peer closes a drainable bead that is in no batch.
	shellOut(t, ws, "bd", "close", drain, "-r", "peer finished it")

	rout := mustPB(t, bin, "unstick", "report", "--workdir", workdir, "--root", ws,
		"--now", reportNow.Format(time.RFC3339), "--json")
	var rep unstick.Report
	if err := json.Unmarshal([]byte(rout), &rep); err != nil {
		t.Fatalf("report json: %v: %s", err, rout)
	}
	attrib := map[string]string{}
	for _, cb := range rep.Closed {
		attrib[cb.ID] = cb.Attribution
	}
	if attrib[future] != unstick.AttribSweep {
		t.Errorf("%s closed by the sweep (batched + listed in results): attribution = %q", future, attrib[future])
	}
	if attrib[drain] != unstick.AttribPeer {
		t.Errorf("%s closed outside any batch: attribution = %q, want peer", drain, attrib[drain])
	}
	var gotMarker bool
	for _, g := range rep.MarkersByOutcome {
		if g.Outcome == "retargeted" && slices.Equal(g.IDs, []string{malformed}) {
			gotMarker = true
		}
	}
	if !gotMarker {
		t.Errorf("markers_by_outcome = %+v, want retargeted -> [%s]", rep.MarkersByOutcome, malformed)
	}
	if rep.SweepN != 1 || rep.PeerN < 1 {
		t.Errorf("sweep/peer changed = %d/%d (want sweep 1 = the closed bead; marker-only changes are not status changes; peer >= 1): %s", rep.SweepN, rep.PeerN, rout)
	}
	if _, err := os.Stat(filepath.Join(workdir, "export.post.jsonl")); err != nil {
		t.Errorf("missing export.post.jsonl: %v", err)
	}
	hout := mustPB(t, bin, "unstick", "report", "--workdir", workdir, "--root", ws, "--now", reportNow.Format(time.RFC3339))
	if !strings.Contains(hout, "HEURISTIC") {
		t.Errorf("human report must state the attribution heuristic:\n%s", hout)
	}
}
