//go:build contract

package internal

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// TestBackend_List_PerPRExpressions_RealBD pins, against a REAL bd in a
// disposable tracker (V-3: fakeRunner cannot prove bd's metadata matching),
// the semantics the per-PR query relies on (bead pg2-eclw3j.3; parity spike
// pg2-eclw3j.1): E1 repo+pr_number metadata, E2 focus item by
// source_type+source_id, E3 title substring, each with --all.
func TestBackend_List_PerPRExpressions_RealBD(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}
	dir := t.TempDir()
	env := cleanBDEnv(t)
	prefix := "tp" + fmt.Sprintf("%x", time.Now().UnixNano())[:10]
	bd := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bd", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	bd("init", "--prefix", prefix, "--non-interactive", "-q", "--skip-agents", "--skip-hooks")

	// create makes one bead and returns its id.
	create := func(title, metadata string) string {
		t.Helper()
		args := []string{"create", title, "--type", "task", "--silent"}
		if metadata != "" {
			args = append(args, "--metadata", metadata)
		}
		id := strings.TrimSpace(bd(args...))
		if id == "" {
			t.Fatalf("create %q returned no id", title)
		}
		return id
	}

	strNum := create("anchor string number", `{"repo":"o/r","pr_number":"5"}`)
	jsonNum := create("anchor json number", `{"repo":"o/r","pr_number":5}`)
	closed := create("closed child", `{"repo":"o/r","pr_number":"5"}`)
	bd("close", closed, "--reason", "done")
	otherPR := create("other pr", `{"repo":"o/r","pr_number":"6"}`)
	otherRepo := create("other repo", `{"repo":"o/other","pr_number":"5"}`)
	focus := create("focus item", `{"source_type":"pr","source_id":"o/r#5"}`)
	focusOther := create("focus item elsewhere", `{"source_type":"pr","source_id":"o/r#6"}`)
	legacy := create("legacy review of o/r#5 (keyless)", "")
	falsePositive := create("mentions o/r#55 only", "")
	focusNotPR := create("not a pr source", `{"source_type":"issue","source_id":"o/r#5"}`)

	b := New(&CLIRunner{Dir: dir, Env: env, Actor: realBDTestActor})
	ctx, cancel := context.WithTimeout(context.Background(), 4*runDeadline)
	defer cancel()
	def := schema.QueryDef{Params: map[string]string{"repo": `[a-z]/[a-z]+`, "pr_number": `[0-9]{1,9}`}}
	values := map[string]string{"repo": "o/r", "pr_number": "5"}
	pctx := schema.WithQueryParams(ctx, schema.QueryParams{Def: def, Values: values})

	ids := func(expr string) []string {
		t.Helper()
		res, err := b.List(pctx, schema.QueryExpr{expr}, true, nil)
		if err != nil {
			t.Fatalf("List %q: %v", expr, err)
		}
		out := append([]string(nil), res.PresentIDs...)
		sort.Strings(out)
		return out
	}
	want := func(in ...string) []string { sort.Strings(in); return in }
	check := func(name string, got, wantIDs []string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(wantIDs, ",") {
			t.Errorf("%s: got %v, want %v", name, got, wantIDs)
		}
	}

	// E1: string and number pr_number both match; the closed bead is returned
	// because --all rides with --metadata-field; other PR/repo are not.
	check("E1", ids("list --all --metadata-field repo={repo} --metadata-field pr_number={pr_number}"),
		want(strNum, jsonNum, closed))
	// E1 without --all drops the closed bead: the --all in the expression matters.
	check("E1 without --all", ids("list --metadata-field repo={repo} --metadata-field pr_number={pr_number}"),
		want(strNum, jsonNum))
	// E2: a focus item by source_type=pr + source_id, not another PR and not a
	// non-pr source type.
	check("E2", ids("list --all --metadata-field source_type=pr --metadata-field source_id={repo}#{pr_number}"),
		want(focus))
	// E3: title substring, case-insensitive; it finds the keyless legacy bead
	// and the focus-item-free titles, and its raw rows include a substring
	// false positive (o/r#55) that the decider's belongs() filter removes.
	e3 := ids("list --all --title-contains {repo}#{pr_number}")
	check("E3", e3, want(legacy, falsePositive))
	_ = []string{otherPR, otherRepo, focusOther, focusNotPR}
}
