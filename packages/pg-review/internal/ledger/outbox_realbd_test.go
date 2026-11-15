package ledger

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// realBdTracker makes a throwaway tracker root with the real bd (embedded
// Dolt, so no server is started) and returns its path. It skips when bd is
// not installed. Every variable that could point bd at a shared tracker is
// removed first.
func realBdTracker(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd is not on PATH; skipping the real-bd filing test")
	}
	for _, k := range []string{"BEADS_DIR", "BEADS_DB", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PASSWORD"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BD_NON_INTERACTIVE", "1")
	root := t.TempDir()
	for _, args := range [][]string{{"git", "init", "-q", "."}, {"bd", "init", "--non-interactive", "--quiet", "-p", "rb"}} {
		c := exec.CommandContext(t.Context(), args[0], args[1:]...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("cannot create a throwaway tracker (%v): %s", err, out)
		}
	}
	return root
}

type realBead struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	ExternalRef string   `json:"external_ref"`
	Labels      []string `json:"labels"`
}

func listRealBeads(t *testing.T, root string) []realBead {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "bd", "-C", root, "list", "--all", "--json").Output()
	if err != nil {
		t.Fatalf("bd list: %v", err)
	}
	var env struct {
		Data []realBead `json:"data"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		var bare []realBead
		if err2 := json.Unmarshal(out, &bare); err2 != nil {
			t.Fatalf("unreadable bd list: %v: %s", err, out)
		}
		return bare
	}
	return env.Data
}

// TestAdoptedFilingAgainstRealBd runs the Adopted filing path against the real
// bd in a throwaway tracker: flag shapes, label, external ref, body on stdin,
// the id read back, and the idempotency lookup after a simulated crash.
func TestAdoptedFilingAgainstRealBd(t *testing.T) {
	root := realBdTracker(t)
	l, c := openClock(t)
	_, d := adoptedIn(t, l, "agent-support", root)

	// A crash after the tracker call, before the row update.
	claimed, ok, err := l.claimDecision(ctx, d.ID)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	f := BdFiler{}
	if _, found, err := f.Find(ctx, root, claimed.BeadIdempotencyKey); err != nil || found {
		t.Fatalf("find in an empty tracker: found=%v err=%v", found, err)
	}
	crashID, err := f.Create(ctx, BeadRequest{Key: claimed.BeadIdempotencyKey, Tracker: root, Label: "agent-support", Title: "crash title", Body: "crash body"})
	if err != nil || crashID == "" {
		t.Fatalf("create: %q %v", crashID, err)
	}
	if id, found, err := f.Find(ctx, root, claimed.BeadIdempotencyKey); err != nil || !found || id != crashID {
		t.Fatalf("find after create: %q %v %v", id, found, err)
	}

	c.Advance(BeadLease + time.Second)
	res, err := FileAdoptedBeads(ctx, l, f)
	if err != nil || res.Filed != 1 {
		t.Fatalf("retry: %+v %v", res, err)
	}
	beads := listRealBeads(t, root)
	if len(beads) != 1 {
		t.Fatalf("want one bead after the retry, got %d: %+v", len(beads), beads)
	}
	b := beads[0]
	if b.ID != crashID || b.ExternalRef != claimed.BeadIdempotencyKey || len(b.Labels) != 1 || b.Labels[0] != "agent-support" {
		t.Errorf("bead: %+v", b)
	}
	if got, _ := l.GetDecision(ctx, d.ID); got.BeadState != BeadFiled || got.WorkBead != crashID {
		t.Errorf("decision: %+v", got)
	}
}

// TestAdoptedBeadBodyAgainstRealBd checks the body reaches the real bd on
// stdin, with the untrusted-origin header, and the title is set.
func TestAdoptedBeadBodyAgainstRealBd(t *testing.T) {
	root := realBdTracker(t)
	l, _ := openClock(t)
	_, d := adoptedIn(t, l, "agent-support", root)
	if res, err := FileAdoptedBeads(ctx, l, BdFiler{}); err != nil || res.Filed != 1 {
		t.Fatalf("filing: %+v %v", res, err)
	}
	beads := listRealBeads(t, root)
	if len(beads) != 1 || beads[0].ExternalRef != d.BeadIdempotencyKey {
		t.Fatalf("beads: %+v", beads)
	}
	if !strings.HasPrefix(beads[0].Description, "Untrusted origin: written by an automated analyst") {
		t.Errorf("body head: %q", firstLine(beads[0].Description))
	}
	if beads[0].Title == "" {
		t.Error("empty title")
	}
}
