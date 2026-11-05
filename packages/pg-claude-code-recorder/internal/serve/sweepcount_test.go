package serve

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func ingestCount(d *Daemon, result string) float64 {
	v, _ := d.obs.Reg.Counter(obs.MIngestResults, map[string]string{"result": result})
	return v
}

func sweepCount(d *Daemon, outcome string) float64 {
	v, _ := d.obs.Reg.Counter(obs.MSweepFiles, map[string]string{"outcome": outcome})
	return v
}

// The real watcher sweep over three transcripts in a temp directory with a real
// store: the first sweep copies all three, the second finds nothing to do and
// every file is an unchanged candidate, and appending to one file makes it
// copied while the other two stay candidates.
func TestSweepCountersOverRealDirectoryAndStore(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	var paths []string
	for _, n := range []string{"a.jsonl", "b.jsonl", "c.jsonl"} {
		paths = append(paths, e.transcript(n, "{\"type\":\"user\"}\n", old))
	}
	d := e.open(t)
	sweep := func() {
		t.Helper()
		if err := d.Watcher().Rescan(); err != nil {
			t.Fatal(err)
		}
	}
	snap := func() map[string]float64 {
		m := map[string]float64{"walked": sweepCount(d, obs.SweepWalked), "cand": sweepCount(d, obs.SweepUnchangedCandidate)}
		for _, r := range obs.IngestResults {
			m[r] = ingestCount(d, r)
		}
		return m
	}
	delta := func(a, b map[string]float64, k string) float64 { return b[k] - a[k] }

	s0 := snap()
	sweep()
	s1 := snap()
	if got := delta(s0, s1, "new_generation"); got != 3 {
		t.Errorf("first sweep new_generation = %v, want 3", got)
	}
	if got := delta(s0, s1, "walked"); got != 3 {
		t.Errorf("first sweep walked = %v, want 3", got)
	}
	if got := delta(s0, s1, "cand"); got != 0 {
		t.Errorf("first sweep unchanged_candidate = %v, want 0 (nothing was observed before)", got)
	}

	sweep()
	s2 := snap()
	if got := delta(s1, s2, "unchanged"); got != 0 {
		t.Errorf("second sweep ingested %v unchanged files, want 0 (skipped)", got)
	}
	if got := delta(s1, s2, "cand"); got != 3 {
		t.Errorf("second sweep unchanged_candidate = %v, want 3", got)
	}
	if got := delta(s1, s2, "new_generation") + delta(s1, s2, "copied"); got != 0 {
		t.Errorf("second sweep copied or started %v files, want 0", got)
	}

	f, err := os.OpenFile(paths[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"type\":\"user\"}\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	sweep()
	s3 := snap()
	if got := delta(s2, s3, "copied"); got != 1 {
		t.Errorf("third sweep copied = %v, want 1", got)
	}
	if got := delta(s2, s3, "unchanged"); got != 0 {
		t.Errorf("third sweep ingested %v unchanged files, want 0 (skipped)", got)
	}
	if got := delta(s2, s3, "cand"); got != 2 {
		t.Errorf("third sweep unchanged_candidate = %v, want 2", got)
	}
	if got := delta(s2, s3, "walked"); got != 3 {
		t.Errorf("third sweep walked = %v, want 3", got)
	}
	// An ingest that comes back after the sweep (the file changed again) stays
	// a candidate only while size, mtime and inode all match: a same-size
	// rewrite with a new mtime is not one.
	if err := os.WriteFile(paths[1], []byte("{\"type\":\"user\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(paths[1], later, later); err != nil {
		t.Fatal(err)
	}
	sweep()
	s4 := snap()
	if got := delta(s3, s4, "cand"); got != 2 {
		t.Errorf("same-size rewrite with a new mtime: unchanged_candidate = %v, want 2", got)
	}
}

// The result label mapping: hyphens become underscores, and every action the
// copier returns is one of the enumerated values.
func TestIngestResultLabelMapsEveryCopierAction(t *testing.T) {
	if got := obs.IngestResultLabel("new-generation"); got != "new_generation" {
		t.Fatalf("new-generation maps to %q", got)
	}
	if got := obs.IngestResultLabel("copied"); got != "copied" {
		t.Fatalf("copied maps to %q", got)
	}
	re := regexp.MustCompile(`Action(?::\s*|\s*=\s*)"([a-z-]+)"`)
	found := map[string]bool{}
	files, _ := filepath.Glob("../copier/*.go")
	for _, f := range files {
		if filepath.Ext(f) != ".go" || regexp.MustCompile(`_test\.go$`).MatchString(f) {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = true
		}
	}
	if len(found) < 9 {
		t.Fatalf("found only %v action literals; the scan is mis-scoped", found)
	}
	for a := range found {
		if l := obs.IngestResultLabel(a); !slices.Contains(obs.IngestResults, l) {
			t.Errorf("copier action %q maps to %q, which is not enumerated in obs.IngestResults", a, l)
		}
	}
	for _, l := range obs.IngestResults {
		if l == obs.ResultError {
			continue
		}
		hyphen := slices.ContainsFunc(slices.Collect(mapKeys(found)), func(a string) bool { return obs.IngestResultLabel(a) == l })
		if !hyphen {
			t.Errorf("enumerated result %q is not a copier action", l)
		}
	}
}

func mapKeys(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// An ingest that returns an error counts as result="error".
func TestFailedIngestCountsAsError(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	before := ingestCount(d, obs.ResultError)
	d.afterIngest("/x", copierResult(), os.ErrNotExist)
	if got := ingestCount(d, obs.ResultError) - before; got != 1 {
		t.Fatalf("error count rose by %v, want 1", got)
	}
}

// The T3 addition on the per-event path is one registry lock acquisition (the
// result counter); the lock waits add none while the locks are free. The
// measured total is logged: it is dominated by the copier's own gauge sets.
func TestIngestPathRegistryLocksAreMeasured(t *testing.T) {
	e := newEnv(t, false)
	p := e.transcript("a.jsonl", "{\"type\":\"user\"}\n", time.Now().Add(-time.Hour))
	d := e.open(t)
	d.ingest(p) // first copy

	appendLine := func() {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("{\"type\":\"user\"}\n")
		_ = f.Close()
	}
	count := func(fn func()) int {
		n := 0
		d.obs.Reg.OnLock = func() { n++ }
		fn()
		d.obs.Reg.OnLock = nil
		return n
	}
	appendLine()
	copierAlone := count(func() { _, _ = d.cp.Ingest(p) })
	appendLine()
	whole := count(func() { d.ingest(p) })
	counter := count(func() { d.obs.Reg.Add(obs.MIngestResults, map[string]string{"result": "copied"}, 1) })
	hist := count(func() { d.obs.Reg.ObserveL(obs.MLoopDuration, ingestLabels, 0) })
	t.Logf("registry lock acquisitions per copying ingest: copier alone %d; whole d.ingest %d (copier %d + loop histogram %d + result counter %d)",
		copierAlone, whole, copierAlone, hist, counter)
	if counter != 1 {
		t.Fatalf("the result counter takes %d registry locks, want 1", counter)
	}
	if whole != copierAlone+hist+counter {
		t.Fatalf("whole ingest took %d locks, want copier %d + histogram %d + counter %d (the lock waits must add none while uncontended)",
			whole, copierAlone, hist, counter)
	}
}

func copierResult() copier.Result { return copier.Result{} }

// A same-size, same-mtime rewrite is invisible to the incremental sweep; the
// periodic full verification (the cache older than verifyEvery) ingests every
// file again and so finds it.
func TestSweepSkipsUnchangedUntilFullVerification(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	p := e.transcript("a.jsonl", "{\"type\":\"user\"}\n", old)
	d := e.open(t)
	sweep := func() {
		t.Helper()
		if err := d.Watcher().Rescan(); err != nil {
			t.Fatal(err)
		}
	}
	sweep() // first copy
	sweep() // caught up: cached
	before := ingestCount(d, "unchanged")
	sweep()
	if got := ingestCount(d, "unchanged") - before; got != 0 {
		t.Fatalf("cached sweep ingested %v files, want 0", got)
	}
	// Same size and mtime, different bytes: not detected by a skipping sweep.
	if err := os.WriteFile(p, []byte("{\"type\":\"asst\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	sweep()
	if got := ingestCount(d, "unchanged") - before; got != 0 {
		t.Fatalf("sweep ingested %v files after a same-size same-mtime rewrite, want 0 (documented limit)", got)
	}
	// Age the verification: the next sweep ingests everything again.
	d.seen.mu.Lock()
	d.seen.verifiedAt = d.clock.Now().Add(-verifyEvery - time.Minute)
	d.seen.mu.Unlock()
	sweep()
	after := ingestCount(d, "unchanged") + ingestCount(d, "new_generation") + ingestCount(d, "copied")
	if after-before < 1 {
		t.Fatalf("verification sweep ingested nothing")
	}
}

// A snapshot-mode file (a tool result here) that the copier found unchanged is
// as caught up as an append file: the next sweep must skip it instead of
// reading and hashing it again, and a change to it must still be copied.
func TestSweepSkipsUnchangedSnapshotFiles(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	dir := filepath.Join(e.claude, "projects", "example-proj", "0f8fad5b-d9cb-469f-a165-70867728950e", "tool-results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "out.txt")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	write("result one\n")
	e.transcript("a.jsonl", "{\"type\":\"user\"}\n", old)
	d := e.open(t)
	sweep := func() {
		t.Helper()
		if err := d.Watcher().Rescan(); err != nil {
			t.Fatal(err)
		}
	}
	sweep() // first copy of both
	sweep() // ingested to unchanged and cached
	before := ingestCount(d, "unchanged")
	sweep()
	if got := ingestCount(d, "unchanged") - before; got != 0 {
		t.Fatalf("a sweep over unchanged files ingested %v of them, want 0 (snapshot and append alike)", got)
	}
	// A real change (new size) is not skipped.
	write("result one and more\n")
	snapBefore := ingestCount(d, "snapshot")
	sweep()
	if got := ingestCount(d, "snapshot") - snapBefore; got != 1 {
		t.Fatalf("a changed snapshot file was stored %v times, want 1", got)
	}
}
