package cmddesc

import (
	"reflect"
	"strings"
	"testing"
)

// interpretDefault interprets command through DefaultRegistry() exactly the way
// the graph builder would (basename lookup, the schema's own interpreter).
func interpretDefault(t *testing.T, command string) Interpretation {
	t.Helper()
	l := leaf(t, command)
	base := l.Executable
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	schema, ok := DefaultRegistry().Lookup(base)
	if !ok {
		t.Fatalf("no schema registered for %q", base)
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatalf("no interpreter %q", schema.Interpreter)
	}
	return in.Interpret(l, schema, Context{})
}

func effectPaths(effects []Effect, access PathAccess) []string {
	var out []string
	for _, e := range effects {
		if e.Kind == EffectPath && e.Access == access {
			out = append(out, e.Path)
		}
	}
	return out
}

// TestCommandDirRebasesRelativePaths: KindCommandDir (`git -C <dir>`, `bd -C
// <dir>`) is a PER-COMMAND working directory — a metadata read of <dir> plus a
// re-base of the command's own RELATIVE path effects — and, unlike cd, it does
// not become a shell chdir (no EffectChdir is emitted).
func TestCommandDirRebasesRelativePaths(t *testing.T) {
	got := interpretDefault(t, "git -C /repo/wt add flake.lock")
	if !got.Sufficient {
		t.Fatalf("insufficient: %s", got.Insufficiency)
	}
	if reads := effectPaths(got.Effects, AccessRead); !reflect.DeepEqual(reads, []string{"/repo/wt", "/repo/wt/flake.lock"}) {
		t.Errorf("reads = %v, want the dir itself and the re-based pathspec", reads)
	}
	if mods := effectPaths(got.Effects, AccessModify); !reflect.DeepEqual(mods, []string{"/repo/wt/.git"}) {
		t.Errorf("modifies = %v, want the implicit .git re-based under the command dir", mods)
	}
	for _, e := range got.Effects {
		if e.Kind == EffectChdir {
			t.Errorf("git -C emitted an EffectChdir %+v: -C must not move the shell", e)
		}
	}
}

func TestCommandDirComposesAndAbsolutePathsStay(t *testing.T) {
	got := interpretDefault(t, "git -C /repo -C sub add /elsewhere/x y")
	if !got.Sufficient {
		t.Fatalf("insufficient: %s", got.Insufficiency)
	}
	reads := effectPaths(got.Effects, AccessRead)
	want := []string{"/repo", "sub", "/elsewhere/x", "/repo/sub/y"}
	if !reflect.DeepEqual(reads, want) {
		t.Errorf("reads = %v, want %v (the second -C is relative to the first; an absolute operand is untouched)", reads, want)
	}
}

func TestCommandDirDynamicMakesRelativePathsDynamic(t *testing.T) {
	got := interpretDefault(t, `git -C "$D" add flake.lock /abs/x`)
	var dynamicRel, staticAbs bool
	for _, e := range got.Effects {
		if e.Kind != EffectPath {
			continue
		}
		if e.Path == "flake.lock" && e.Dynamic {
			dynamicRel = true
		}
		if e.Path == "/abs/x" && !e.Dynamic {
			staticAbs = true
		}
	}
	if !dynamicRel || !staticAbs {
		t.Errorf("dynamic -C: relative operand dynamic=%v, absolute operand static=%v (want both true): %+v", dynamicRel, staticAbs, got.Effects)
	}
}

// TestGlobalFlagOperandsAreResolved: a subcommand schema's GLOBAL flags'
// operands used to be collected by scanGlobal and then silently dropped, so
// `bd --db <path> list` never read <path> and `git -c <bad pair> status` never
// reached the closed-set check. They are resolved now.
func TestGlobalFlagOperandsAreResolved(t *testing.T) {
	got := interpretDefault(t, "bd --db /secret/db.sqlite list")
	if reads := effectPaths(got.Effects, AccessRead); !reflect.DeepEqual(reads, []string{"/secret/db.sqlite"}) {
		t.Errorf("bd --db: reads = %v, want the --db path read to be emitted", reads)
	}
	if bad := interpretDefault(t, "git -c core.pager=evil status"); bad.Sufficient {
		t.Error("git -c core.pager=evil status: sufficient, want the closed-set check to fail it")
	}
}

func TestGitConfigPairClosedSet(t *testing.T) {
	ok := []string{
		"git -c rerere.enabled=false log",
		"git -c core.fsmonitor=false -c core.editor=true status",
		"git -c sequence.editor=: log",
		"git -crerere.enabled=true log",
	}
	for _, c := range ok {
		if got := interpretDefault(t, c); !got.Sufficient {
			t.Errorf("%q: insufficient (%s), want sufficient", c, got.Insufficiency)
		}
	}
	bad := []string{
		"git -c core.fsmonitor=/tmp/x.sh status",      // a hook program, not a boolean
		"git -c core.editor=/tmp/evil.sh status",      // non-inert editor
		"git -c alias.ci=commit ci",                   // alias injection
		"git -c core.pager=less log",                  // pager exec
		"git -c Rerere.Enabled=false log",             // not the exact spelling: closed means closed
		"git -c rerere.enabled=false -c core.x=1 log", // all-or-nothing
		`git -c "$PAIR" log`,                          // runtime expansion
	}
	for _, c := range bad {
		if got := interpretDefault(t, c); got.Sufficient {
			t.Errorf("%q: sufficient, want insufficient (the pair is outside the closed inert set)", c)
		}
	}
}

func TestRetargetRemoteTransform(t *testing.T) {
	// gh pr create: --draft upgrades the operation, in either flag position.
	for _, c := range []string{"gh pr create --draft --title t", "gh pr create --title t --draft", "gh pr create -d --fill"} {
		got := interpretDefault(t, c)
		if !got.Sufficient || !hasRemoteOp(got.Effects, "pr-create-draft") || hasRemoteOp(got.Effects, "pr-create") {
			t.Errorf("%q: want only a pr-create-draft remote effect, got %+v", c, got.Effects)
		}
	}
	got := interpretDefault(t, "gh pr create --title t")
	if !hasRemoteOp(got.Effects, "pr-create") || hasRemoteOp(got.Effects, "pr-create-draft") {
		t.Errorf("non-draft create: want the pr-create operation, got %+v", got.Effects)
	}
	// An empty From/To fails closed instead of passing effects through.
	if _, ok := applyTransform(EffectTransform{Kind: TransformRetargetRemote}, nil); ok {
		t.Error("retarget-remote with empty From/To must fail closed")
	}
}

func hasRemoteOp(effects []Effect, op string) bool {
	for _, e := range effects {
		if e.Kind == EffectRemote && e.Operation == op {
			return true
		}
	}
	return false
}

// TestPushOperationLadder pins which remote operation each push spelling
// resolves to, in every flag order — the data the R6 policy keys on. A lease
// flag must never soften a force/delete on the same line; the bulk flags must
// win over a lease.
func TestPushOperationLadder(t *testing.T) {
	cases := map[string]string{
		"git push origin main": "push",
		"git push":             "push",
		"git push --force-with-lease origin main":          "push-lease",
		"git push --force origin main":                     "force-push",
		"git push -fu origin main":                         "force-push",
		"git push --force-with-lease --force origin main":  "force-push",
		"git push --force --force-with-lease origin main":  "force-push",
		"git push --force-with-lease --delete origin main": "delete-ref",
		"git push --delete --force-with-lease origin main": "delete-ref",
		"git push --all origin":                            "push-bulk",
		"git push --force-with-lease --all origin":         "push-bulk",
		"git push --all --force-with-lease origin":         "push-bulk",
		"git push --tags origin":                           "push-bulk",
		"git push --prune origin":                          "push-bulk",
		"git push --mirror origin":                         "push-bulk",
		"git push --branches origin":                       "push-bulk",
	}
	for command, want := range cases {
		got := interpretDefault(t, command)
		if !got.Sufficient {
			t.Errorf("%q: insufficient: %s", command, got.Insufficiency)
			continue
		}
		var ops []string
		for _, e := range got.Effects {
			if e.Kind == EffectRemote {
				ops = append(ops, e.Operation)
			}
		}
		if !reflect.DeepEqual(ops, []string{want}) {
			t.Errorf("%q: remote ops = %v, want [%s]", command, ops, want)
		}
	}
	insufficient := []string{
		"git push origin +main",                       // force via refspec
		"git push origin :main",                       // delete via refspec
		"git push origin refs/heads/*:refs/heads/*",   // glob
		"git push --force-with-lease=main:abc origin", // cross-branch lease
		"git push --receive-pack=evil origin main",    // program on the remote side
		"git push --exec=evil origin main",
		"git push --no-verify origin main",
	}
	for _, c := range insufficient {
		if got := interpretDefault(t, c); got.Sufficient {
			t.Errorf("%q: sufficient, want insufficient", c)
		}
	}
}

// TestBdVerbClasses pins the Operation class of the bd verbs the skills use
// and of their un-instructed neighbours: the instructed bookkeeping verbs are
// tracker-write, the rest of the mutators stay "mutate" (consent), Dolt
// lifecycle stays dolt-server.
func TestBdVerbClasses(t *testing.T) {
	cases := map[string]string{
		"bd list":                     "read",
		"bd show x":                   "read",
		"bd stats":                    "read",
		"bd version":                  "read",
		"bd dep list x":               "read",
		"bd comments x":               "read",
		"bd human list":               "read",
		"bd create t":                 "tracker-write",
		"bd update x --claim":         "tracker-write",
		"bd close x --reason r":       "tracker-write",
		"bd comment x text":           "tracker-write",
		"bd comments add x text":      "tracker-write",
		"bd q text":                   "tracker-write",
		"bd defer x":                  "tracker-write",
		"bd undefer x":                "tracker-write",
		"bd dep add a --blocked-by b": "tracker-write",
		"bd label add x l":            "tracker-write",
		"bd label remove x l":         "tracker-write",
		"bd reopen x":                 "mutate",
		"bd delete x":                 "mutate",
		"bd dep remove a b":           "mutate",
		"bd human dismiss x":          "mutate",
		"bd dolt commit":              "mutate",
		"bd dolt start":               "dolt-server",
	}
	for command, want := range cases {
		got := interpretDefault(t, command)
		if !got.Sufficient {
			t.Errorf("%q: insufficient: %s", command, got.Insufficiency)
			continue
		}
		var ops []string
		for _, e := range got.Effects {
			if e.Kind == EffectRemote {
				ops = append(ops, e.Operation)
			}
		}
		if !reflect.DeepEqual(ops, []string{want}) {
			t.Errorf("%q: remote ops = %v, want [%s]", command, ops, want)
		}
	}
}

// TestBdFileFlagsAreReadsPerVerb: a bd tracker-write verb's file-reading flag
// is a modeled read ONLY on the verbs whose own --help lists it; bd close -f
// is --force, not a file.
func TestBdFileFlagsAreReadsPerVerb(t *testing.T) {
	read := func(command string) []string {
		return effectPaths(interpretDefault(t, command).Effects, AccessRead)
	}
	if got := read("bd update x --body-file notes.md"); !reflect.DeepEqual(got, []string{"notes.md"}) {
		t.Errorf("update --body-file reads = %v", got)
	}
	if got := read("bd create t -f plan.md"); !reflect.DeepEqual(got, []string{"plan.md"}) {
		t.Errorf("create -f reads = %v", got)
	}
	if got := read("bd close x -f"); len(got) != 0 {
		t.Errorf("bd close -f (--force) must not be a file read, reads = %v", got)
	}
	if got := read("bd comments add x -f note.txt"); !reflect.DeepEqual(got, []string{"note.txt"}) {
		t.Errorf("comments add -f reads = %v", got)
	}
	if got := effectPaths(interpretDefault(t, "bd export -o out.jsonl").Effects, AccessTruncate); !reflect.DeepEqual(got, []string{"out.jsonl"}) {
		t.Errorf("bd export -o truncates %v, want [out.jsonl]", got)
	}
}

// TestPluginToolSchemasFailClosed: every schema this slice added refuses an
// unlisted flag (the ceta-spec-gen rule: skill-generated specs are never
// UnknownFlagInert) and is registered under a unique name.
func TestPluginToolSchemasFailClosed(t *testing.T) {
	seen := map[string]bool{}
	var walk func(path string, s CommandSchema)
	walk = func(path string, s CommandSchema) {
		if s.UnknownFlag != UnknownFlagInsufficient {
			t.Errorf("%s: UnknownFlag = %v, want insufficient (fail closed)", path, s.UnknownFlag)
		}
		for n, sub := range s.Subcommands {
			walk(path+" "+n, sub)
		}
	}
	for _, s := range pluginToolSchemas() {
		if seen[s.Name] {
			t.Errorf("duplicate plugin tool schema %q", s.Name)
		}
		seen[s.Name] = true
		walk(s.Name, s)
	}
}

// TestPluginToolSpotChecks: one representative form per tool resolves to the
// effect class the schema documents, and a representative dangerous neighbour
// is insufficient.
func TestPluginToolSpotChecks(t *testing.T) {
	sufficient := []string{
		"bgcheck flake-check", "bgcheck", "integrate-branch-support --facts", "integrate-branch-support --prek-branch-diff",
		"wtdone feature --cc /repo", "session-mode start wrap-up-session", "session-mode set-status finished --handoff-bead pg2-abc",
		"session-mode show", "handoff-create --attended --session-id s --title t --body-file b.md",
		"pg-go-mutate ./internal/collect --workers 1", "pg-ccaudit query top-signatures --since 2026-07-22 --until 2026-07-30",
		"pg-ccaudit classify status --since a --until b", "pg-ccaudit report --classifier cli --max 100 --since a --until b",
		"pg-wi-flow next --stage s", "pg-wi-flow resolve x --defer 2026-10-10", "pg-hooks run pre-commit a.go b.go",
		"pg-hooks status --porcelain", "pg-hooks fix", "prek run --files a b", "prek run --all-files", "bats tests/",
		"gh pr view feature --json url,state,number", "gh pr create --draft --head f --base main --fill",
		"pn workspace push", "pn workspace update", "pn workspace apply", "pn workspace doctor",
		"pn workspace workforest list", "pn workspace workforest prune",
		"claude-extended-tool-approver evaluate --misses-only --format=json", "claude-extended-tool-approver show 1 2 --format=json",
		"claude-extended-tool-approver lint --embedded", "claude-extended-tool-approver spec-drift-check --embedded",
		"rg -uu -in x -g '*y*' /repo", "fd -HI artifact", "du -sh dir", "lsof -nP -iTCP:25252", "launchctl list org.x", "man jq",
		"nix build .#checks.aarch64-darwin.x", "nix flake check", "nix flake update nixpkgs", "nix flake lock",
		"create-packet.sh --parent p --title t --body-file b --acceptance a", "chunk-for-bd-field.sh in out",
		"audit-docket-label-leak.sh --json", "impl-traces.sh --strict set impl", "resolve-imports.sh o i",
		"name-collisions.sh a b c", "self-checks.sh d", "trace-extract.sh d", "relocation-check.sh d", "capture-prefix-snapshots.sh out",
		"git merge-base --is-ancestor a b", "git cherry -v main f", "git ls-tree -r --name-only main -- p", "git ls-files d",
		"git grep -c -- sym main -- p", "git symbolic-ref --short refs/remotes/origin/HEAD", "git remote", "git stash list",
		"git fetch --quiet origin", "git rebase --continue", "git merge --ff-only f", "git checkout --theirs -- flake.lock",
		"git branch -d f", "git range-diff a..b c..d", "git diff --no-ext-diff --name-only --diff-filter=U",
		"git diff --no-index a b", "git rev-parse --path-format=absolute --git-common-dir", "git rev-list --count a..b",
		"git -c rerere.enabled=false -C /repo rebase main",
	}
	for _, c := range sufficient {
		if got := interpretDefault(t, c); !got.Sufficient {
			t.Errorf("%q: insufficient (%s), want sufficient", c, got.Insufficiency)
		}
	}
	insufficient := []string{
		"pg-ccaudit ingest", "pg-ccaudit query q -db /tmp/x", "launchctl bootout x", "gh pr merge 1", "gh pr ready 1",
		"gh pr view 1 --web", "pn workspace push --no-verify", "pn workspace workforest remove --force b",
		"pn workspace doctor --fix", "pn workspace workforest remove b", "prek run -c /tmp/evil.toml", "pg-hooks run pre-commit -- --config x", "bats --formatter /tmp/e t",
		"nix build github:evil/x", "nix build --impure .#x", "nix flake update --commit-lock-file", "rg --pre /tmp/e x",
		"fd -x rm", "session-mode hook session-end", "claude-extended-tool-approver archive",
		"claude-extended-tool-approver spec-drift-check --embedded --record", "git stash pop", "git stash -u",
		"git checkout main", "git rebase -i main", "git rebase --exec x main", "git merge origin/main", "git fetch --upload-pack=x origin",
		"git branch -D f", "git branch f", "git apply p.diff", "git config k v", "man -P less jq",
	}
	for _, c := range insufficient {
		if got := interpretDefault(t, c); got.Sufficient {
			t.Errorf("%q: sufficient, want insufficient (abstain)", c)
		}
	}
}
