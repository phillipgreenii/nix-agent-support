package goldencorpus

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
)

// Phase 4: fixture-backed goldens for path-state-dependent shapes. These tests pin the structural contract of the rows tagged
// "state-dependent"; the verdicts themselves are graded by
// `evaluate --corpus`.

func stateDepRows(t *testing.T) []Row {
	t.Helper()
	var out []Row
	for _, r := range loadCorpus(t) {
		if slices.Contains(r.Tags, "state-dependent") {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		t.Fatal("corpus.json has no row tagged state-dependent")
	}
	return out
}

// TestStateDepRowsAreSyntheticAndPinned: a fixture pins its path state in the
// row, so its working directory is an invented path (never the host's), its
// mode is default and its settings and rules fixtures are empty.
func TestStateDepRowsAreSyntheticAndPinned(t *testing.T) {
	for _, r := range stateDepRows(t) {
		cwd := r.CWDPathState.CWD
		if cwd != "/repo" && !strings.HasPrefix(cwd, "/tmp/ceta-fixture-") {
			t.Errorf("case %q: cwd %q is not a synthetic fixture path", r.Case, cwd)
		}
		if root := r.CWDPathState.ProjectRoot; root != cwd {
			t.Errorf("case %q: project_root %q must equal cwd %q", r.Case, root, cwd)
		}
		if r.CWDPathState.Home != "" {
			t.Errorf("case %q: a fixture row must not set home (a config-consulting shape gets no row)", r.Case)
		}
		if r.Mode != ModeDefault {
			t.Errorf("case %q: mode = %q, want default", r.Case, r.Mode)
		}
		if len(r.Settings.PermissionsAllow)+len(r.Settings.PermissionsDeny)+len(r.Settings.PermissionsAsk) != 0 {
			t.Errorf("case %q: settings must be empty", r.Case)
		}
		if !hasPrefixTag(r.Tags, "family:") {
			t.Errorf("case %q: missing family:<family> tag", r.Case)
		}
	}
}

func hasPrefixTag(tags []string, prefix string) bool {
	for _, tag := range tags {
		if strings.HasPrefix(tag, prefix) {
			return true
		}
	}
	return false
}

// TestStateDepShapeRowsFollowNaming: a row that covers a replayed shape is
// named statedep-<12 hex of sha256(shape_key)> and its notes are
// "shape <shape_key>".
func TestStateDepShapeRowsFollowNaming(t *testing.T) {
	shapeRows := 0
	for _, r := range stateDepRows(t) {
		var keys []string
		for _, tag := range r.Tags {
			if k, ok := strings.CutPrefix(tag, "shape:"); ok {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			continue
		}
		shapeRows++
		if len(keys) != 1 {
			t.Errorf("case %q: want exactly one shape: tag, got %d", r.Case, len(keys))
			continue
		}
		sum := sha256.Sum256([]byte(keys[0]))
		if want := "statedep-" + hex.EncodeToString(sum[:])[:12]; r.Case != want {
			t.Errorf("case %q: want name %q", r.Case, want)
		}
		if want := "shape " + keys[0]; r.Notes != want {
			t.Errorf("case %q: notes = %q, want %q", r.Case, r.Notes, want)
		}
	}
	if shapeRows == 0 {
		t.Error("no state-dependent row covers a replayed shape")
	}
}

// TestStateDepMinimumCases pins the minimum cases that `evaluate --corpus`
// can grade today, with their verdicts. Cases 1 (tracked credential-format
// read) and 3 (.git write in a non-temp repo) are not present: the
// evaluator does not yet materialize cwd_path_state git facts.
func TestStateDepMinimumCases(t *testing.T) {
	want := map[string]struct {
		verdict Verdict
		command string
	}{
		"statedep-min-2": {Reject, "cat .env"},
		"statedep-min-4": {Approve, "echo x > .git/hooks/pre-commit"},
	}
	seen := map[string]bool{}
	for _, r := range stateDepRows(t) {
		w, ok := want[r.Case]
		if !ok {
			continue
		}
		seen[r.Case] = true
		if r.ExpectedVerdict != w.verdict {
			t.Errorf("case %q: verdict %q, want %q", r.Case, r.ExpectedVerdict, w.verdict)
		}
		if got, _ := r.ToolInput.ToolInput["command"].(string); got != w.command {
			t.Errorf("case %q: command %q, want %q", r.Case, got, w.command)
		}
		if hasPrefixTag(r.Tags, "shape:") {
			t.Errorf("case %q: a minimum case carries no shape: tag", r.Case)
		}
	}
	for c := range want {
		if !seen[c] {
			t.Errorf("minimum case %q is missing", c)
		}
	}
	// The temp-repo row differs from its non-temp twin only by a temp root.
	for _, r := range stateDepRows(t) {
		if r.Case == "statedep-min-4" && !strings.HasPrefix(r.CWDPathState.CWD, "/tmp/") {
			t.Errorf("statedep-min-4 must sit under a temp root, cwd = %q", r.CWDPathState.CWD)
		}
	}
}
