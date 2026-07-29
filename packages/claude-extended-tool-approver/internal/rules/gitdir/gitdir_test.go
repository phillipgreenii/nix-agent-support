package gitdir

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// leavesOf mirrors how engine.EvaluateExpression decomposes an expression into the
// per-leaf raw texts it hands to each rule, so an expression-scope test exercises
// the rule exactly as the engine drives it.
func leavesOf(expr string) []string {
	var out []string
	for _, pc := range cmdparse.Parse(expr) {
		out = append(out, pc.Raw)
	}
	return out
}

func bashJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func fileJSON(path string) json.RawMessage {
	b, _ := json.Marshal(hookio.FileToolInput{FilePath: path})
	return b
}

func searchJSON(path string) json.RawMessage {
	b, _ := json.Marshal(hookio.SearchToolInput{Pattern: "x", Path: path})
	return b
}

func bashInput(cmd string) *hookio.HookInput {
	return &hookio.HookInput{ToolName: "Bash", ToolInput: bashJSON(cmd)}
}

func TestGitDir_Bash(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// Writes stay a hard block.
		{"write into .git", "echo x > .git/hooks/pre-commit", hookio.Reject},
		{"append into .git", "echo x >> .git/config", hookio.Reject},
		{"rm .git dir", "rm -rf .git/", hookio.Reject},
		{"rm bare .git", "rm -rf .git", hookio.Reject},
		{"sed -i on .git config", "sed -i 's/a/b/' .git/config", hookio.Reject},
		{"sed -i bundled on .git config", "sed -ri 's/a/b/' .git/config", hookio.Reject},
		{"tee into .git", "tee .git/config", hookio.Reject},
		{"chmod a hook", "chmod +x .git/hooks/pre-commit", hookio.Reject},
		{"truncate a ref", "truncate -s 0 .git/refs/heads/main", hookio.Reject},
		{"cp ONTO .git config", "cp /tmp/evil .git/config", hookio.Reject},
		{"mv ONTO .git config", "mv /tmp/evil .git/config", hookio.Reject},
		{"unknown command fails safe to write", "frobnicate .git/config", hookio.Reject},
		{"exec a hook script", ".git/hooks/pre-commit", hookio.Reject},
		// The exclusion-flag set is named flag-by-flag rather than "any flag's
		// value", precisely so a destructive operand that happens to follow a flag
		// is still caught.
		{"rm operand following a flag still rejects", "rm -rf .git", hookio.Reject},
		{"rm -r operand following a flag still rejects", "rm -r .git/objects", hookio.Reject},
		{"git porcelain with a redirect INTO .git", "git status > .git/stolen", hookio.Reject},

		// Reads are decisive but overridable.
		{"cat .git config", "cat .git/config", hookio.Ask},
		{"nested .git", "cat repo/.git/HEAD", hookio.Ask},
		{"trailing /.git", "ls foo/.git", hookio.Ask},
		{"stat a ref", "stat .git/refs/heads/main", hookio.Ask},
		{"readlink a hook", "readlink .git/hooks/pre-commit", hookio.Ask},
		{"test -e a hook", "[ -e .git/hooks/pre-commit ]", hookio.Ask},
		{"if test -e a hook", "if [ -e .git/hooks/pre-commit ]", hookio.Ask},
		{"wc a ref", "wc -l .git/info/exclude", hookio.Ask},
		{"head a ref", "head -5 .git/HEAD", hookio.Ask},
		{"diff two refs", "diff .git/HEAD /tmp/head", hookio.Ask},
		{"sed WITHOUT -i streams to stdout", "sed -n '1p' .git/config", hookio.Ask},
		{"grep a hook file", "grep prek .git/hooks/pre-commit", hookio.Ask},
		{"cp FROM .git is a read", "cp .git/config /tmp/backup", hookio.Ask},
		{"ln pointing AT .git neither reads nor writes it", "ln -s .git/config /tmp/link", hookio.Ask},
		{"stdin FROM .git is a read", "wc -l < .git/config", hookio.Ask},

		// Non-accesses: nothing is matched at all.
		{"bare git command not blocked", "git status", hookio.Abstain},
		{"git config not blocked", "git config user.name", hookio.Abstain},
		{"gitignore not blocked", "cat .gitignore", hookio.Abstain},
		{"git-suffixed name not blocked", "cat .git.bak", hookio.Abstain},
		{"unrelated", "ls -la", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Evaluate(bashInput(tt.command)).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGitDir_DestructiveOnSourceOperand pins the operand-GEOMETRY half of the
// direction split: a command can be destructive on an operand that a naive
// "last operand is the destination" model reads as a mere source.
//
// The defect this fixes: `mv` was grouped with cp/ln/install, so
// `mv .git/HEAD /tmp/x` classified as a READ and returned Ask. Moving git
// metadata away DESTROYS it at the source — a rename is a write to BOTH operands,
// and downgrading it from a hard block to an overridable prompt is exactly the
// hole the direction split was supposed to close. The original regression guards
// covered `sed -i`, redirections and `rm`, but no rename/move shape at all.
//
// The contrast cases matter as much as the fix: cp/ln/install genuinely only READ
// their source, so those must stay Ask or the split becomes "everything is a
// write" and the Class-3 false positives come straight back.
func TestGitDir_DestructiveOnSourceOperand(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// mv/rename destroy the SOURCE: both operands are writes.
		{"mv gitmeta AWAY", "mv .git/HEAD /tmp/x", hookio.Reject},
		{"mv gitmeta INTO", "mv /tmp/x .git/HEAD", hookio.Reject},
		{"mv the whole dir away", "mv .git /tmp/stash", hookio.Reject},
		{"mv -f a hook away", "mv -f .git/hooks/pre-commit /tmp/", hookio.Reject},
		{"rename over gitmeta", "rename 's/HEAD/OLD/' .git/HEAD", hookio.Reject},
		// A rename bound through a variable is a write too.
		{"bound path is mv'd away", "f=/repo/.git/HEAD\nmv \"$f\" /tmp/x", hookio.Reject},

		// cp/ln/install only READ their source — these MUST stay Ask.
		{"cp FROM gitmeta", "cp .git/config /tmp/backup", hookio.Ask},
		{"cp -p FROM gitmeta", "cp -p .git/config /tmp/backup", hookio.Ask},
		{"ln -s AT gitmeta", "ln -s .git/config /tmp/link", hookio.Ask},
		{"ln -sf AT gitmeta", "ln -sf .git/config /tmp/link", hookio.Ask},
		{"install FROM gitmeta", "install -m 644 .git/config /tmp/out", hookio.Ask},

		// …but writing INTO gitmeta with the same commands is a write.
		{"cp INTO gitmeta", "cp /tmp/evil .git/config", hookio.Reject},
		{"ln -sf CLOBBERS a hook", "ln -sf /tmp/evil .git/hooks/pre-commit", hookio.Reject},
		{"install INTO a hook", "install -m 755 /tmp/evil .git/hooks/pre-commit", hookio.Reject},

		// The destination can also come from a FLAG, inverting the geometry so the
		// last operand is a SOURCE. Fail safe rather than read the wrong end.
		{"install -t INTO gitmeta dir", "install -t .git/hooks /tmp/evil", hookio.Reject},
		{"cp -t INTO gitmeta dir", "cp -t .git/hooks /tmp/evil", hookio.Reject},
		{"install -d CREATES gitmeta dirs", "install -d .git/hooks /tmp/other", hookio.Reject},

		// Commands on the read allowlist that a flag turns into writers.
		{"find -delete", "find .git/objects -type f -delete", hookio.Reject},
		{"find -exec", "find .git -type f -exec rm {} ;", hookio.Reject},
		{"sort -o writes its output file", "sort -o .git/config /tmp/in", hookio.Reject},
		{"yq -i edits in place", "yq -i '.a=1' .git/config", hookio.Reject},
		{"find WITHOUT a mutating flag still reads", "find .git/objects -type f", hookio.Ask},
		{"sort WITHOUT -o still reads", "sort .git/config", hookio.Ask},

		// dd names its operands as key=value, so a component walk over the whole
		// token misses the path entirely.
		{"dd of= writes gitmeta", "dd of=.git/HEAD if=/dev/zero", hookio.Reject},
		{"dd if= reads gitmeta but dd is not on the read list", "dd if=.git/HEAD of=/tmp/x", hookio.Reject},

		// Already-correct destructive commands, pinned so they cannot drift.
		{"truncate", "truncate -s 0 .git/HEAD", hookio.Reject},
		{"rsync --delete INTO gitmeta", "rsync -a --delete /tmp/src/ .git/", hookio.Reject},
		{"tar -x into gitmeta via -C", "tar -xf /tmp/a.tar -C .git", hookio.Reject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fold the leaves most-restrictive-wins with RootExpression set, exactly as
			// engine.EvaluateExpression drives the rule, so the multi-leaf bound-path
			// case is exercised the same way as the single-leaf ones.
			got := hookio.Decision(hookio.Approve)
			for _, leaf := range leavesOf(tt.command) {
				in := &hookio.HookInput{
					ToolName:       "Bash",
					ToolInput:      bashJSON(leaf),
					RootExpression: tt.command,
				}
				if d := r.Evaluate(in).Decision; d > got {
					got = d
				}
			}
			if got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGitDir_ReasonDistinguishesDirection pins that a READ is never described to
// the user as a modification. The old rule emitted "modify git metadata through
// git commands only" for a plain `ls`, which was simply false.
func TestGitDir_ReasonDistinguishesDirection(t *testing.T) {
	r := New()
	read := r.Evaluate(bashInput("ls -la .git/hooks"))
	if read.Decision != hookio.Ask {
		t.Fatalf("read Decision = %v, want Ask", read.Decision)
	}
	if want := "reading git metadata under .git/ requires confirmation"; read.Reason != want {
		t.Errorf("read Reason = %q, want %q", read.Reason, want)
	}
	write := r.Evaluate(bashInput("rm -rf .git/hooks"))
	if write.Decision != hookio.Reject {
		t.Fatalf("write Decision = %v, want Reject", write.Decision)
	}
	if want := "refusing to write git metadata under .git/ directly — modify it through git commands only"; write.Reason != want {
		t.Errorf("write Reason = %q, want %q", write.Reason, want)
	}
}

// TestGitDir_QuotedHeredocBodyIsNotAnAccess pins class 1 of pg2-3hk7t: a
// git-metadata path that appears only as PROSE inside a quoted heredoc body
// performs zero filesystem access and MUST NOT be matched. Corpus row 126856 —
// a notification payload whose bead title mentioned `.git/index` — was hard-DENIED
// by the former raw substring match.
func TestGitDir_QuotedHeredocBodyIsNotAnAccess(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"row 126856 shape: heredoc payload in an assignment", "PAYLOAD=$(cat <<'EOF'\n{\n  \"title\": \"infra-block: ziprecruiter .git/index is 0 bytes\"\n}\nEOF\n)"},
		{"commit message mentioning a path", "git commit -m 'stop reading .git/config directly'"},
		{"echoed prose mentioning a path", "echo 'see .git/config for the remote'"},
		{"bead title in a double-quoted arg", "bd create \"repo .git/index is 0 bytes\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Evaluate(bashInput(tt.command)).Decision; got != hookio.Abstain {
				t.Errorf("Decision = %v, want Abstain (prose is not a path operand)", got)
			}
		})
	}
}

// TestGitDir_ExclusionRolesAreNotAccesses pins class 2 of pg2-3hk7t: a token whose
// SYNTACTIC ROLE is to exclude git metadata proves the command does not touch it.
func TestGitDir_ExclusionRolesAreNotAccesses(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"negated ripgrep glob (rows 288917/288920)", "rg -c mkBashScript /repo -g '!**/.git/**'"},
		{"exclusion glob list in an assignment", "EX=\"-g !**/docs/** -g !**/.git/** --hidden\""},
		{"grep -v filters it OUT (row 244194)", "grep -v \"/.git/\""},
		{"grep -v in a pipeline arg", "grep -rn foo /repo | grep -v \"/.git/\""},
		{"grep PATTERN is not a path", "grep -rn \"/.git/\" /repo"},
		{"find -path … -prune (rows 237022/259890)", "find . -path ./.git -prune -o -type f -print"},
		{"find -name … -prune", "find . -name .git -prune -o -print"},
		{"find NEGATED -path without -prune", "find . -type d ! -path './.git/*'"},
		// Rows 101602/124447: `tree -I '.git'` is a read-only listing that EXCLUDES
		// git metadata. Reading `-I`'s value as an operand made these a NEW hard deny.
		{"tree -I ignore pattern (rows 101602/124447)", "tree -L 3 /repo -I \".git\""},
		{"tree -I before the path operand", "tree -L 3 -I '.git' --charset ascii"},
		{"fd -E exclude pattern", "fd -E .git --type f ."},
		{"grep --exclude-dir separate value", "grep -rn foo /repo --exclude-dir .git"},
		{"grep --exclude-dir glued value", "grep -rn foo /repo --exclude-dir=.git"},
		{"git config -f reads THROUGH git (row 309585)", "git config -f /repo/.git/config --get core.fsmonitor"},
		{"git --git-dir is a git command", "git --git-dir=/repo/.git rev-parse HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Evaluate(bashInput(tt.command)).Decision; got != hookio.Abstain {
				t.Errorf("Decision = %v, want Abstain (exclusion/git-mediated role)", got)
			}
		})
	}
}

// TestGitDir_BoundPathDirectionFollowsItsUse pins class 3 of pg2-3hk7t together
// with its regression guard. An assignment binds a path and accesses nothing, so
// the direction comes from the sibling leaf that CONSUMES the variable — which is
// visible only at EXPRESSION scope (hookio.HookInput.RootExpression). The two
// shapes below are byte-identical at the assignment leaf; only the sibling differs.
func TestGitDir_BoundPathDirectionFollowsItsUse(t *testing.T) {
	r := New()
	tests := []struct {
		name string
		expr string
		want hookio.Decision
	}{
		{
			name: "row 244438 shape: bound path is sed -i'd → still Reject",
			expr: "f=\"$r/.git/info/exclude\"\ncat \"$f\"\nsed -i '' '/^x$/d' \"$f\"",
			want: hookio.Reject,
		},
		{
			name: "row 167117 shape: bound path is only read → Ask",
			expr: "RM=/repo/.git/worktrees/slot-c/rebase-merge\nls -la \"$RM\"\ncat \"$RM/done\"",
			want: hookio.Ask,
		},
		{
			name: "row 163591 shape: bound hooks path read inside a substitution → Ask",
			expr: "h=\"$r/.git/hooks\"\necho \"active -> $(grep -m1 prek \"$h/pre-commit\")\"",
			want: hookio.Ask,
		},
		{
			name: "row 184010 shape: bound hook path tested and readlink'd → Ask",
			expr: "h=\"$r/.git/hooks/pre-commit\"\nif [ -e \"$h\" ]\nthen echo \"present ($(readlink \"$h\"))\"",
			want: hookio.Ask,
		},
		{
			name: "row 305013 shape: default-expansion binding read by ls/head → Ask",
			expr: "HP=\"${HP:-$CC/.git/hooks}\"\nls -la \"$HP/pre-push\"\nhead -5 \"$HP/pre-push\"",
			want: hookio.Ask,
		},
		{
			name: "bound path written by a redirection → Reject",
			expr: "f=\"$r/.git/config\"\necho x > \"$f\"",
			want: hookio.Reject,
		},
		{
			name: "bound path never consumed is undecidable → Reject",
			expr: "f=\"$r/.git/config\"\necho done",
			want: hookio.Reject,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The engine hands the rule one leaf at a time with RootExpression set to
			// the whole expression; fold the leaves most-restrictive-wins exactly as
			// engine.EvaluateExpression does.
			got := hookio.Decision(hookio.Approve)
			for _, leaf := range leavesOf(tt.expr) {
				in := &hookio.HookInput{
					ToolName:       "Bash",
					ToolInput:      bashJSON(leaf),
					RootExpression: tt.expr,
				}
				if d := r.Evaluate(in).Decision; d > got {
					got = d
				}
			}
			if got != tt.want {
				t.Errorf("folded Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGitDir_LeafScopeFailsSafeWithoutRootExpression pins that a bound path with
// NO expression scope available (a direct call, RootExpression empty) still fails
// safe to a write rather than silently becoming a read.
func TestGitDir_LeafScopeFailsSafeWithoutRootExpression(t *testing.T) {
	r := New()
	got := r.Evaluate(bashInput("f=\"$r/.git/info/exclude\"")).Decision
	if got != hookio.Reject {
		t.Errorf("Decision = %v, want Reject (undecidable direction fails safe)", got)
	}
}

func TestGitDir_FileTools(t *testing.T) {
	r := New()
	tests := []struct {
		name string
		tool string
		path string
		want hookio.Decision
	}{
		{"read .git config asks", "Read", "/repo/.git/config", hookio.Ask},
		{"grep .git asks", "Grep", "/repo/.git/objects", hookio.Ask},
		{"glob .git asks", "Glob", "/repo/.git", hookio.Ask},
		{"write .git ref rejects", "Write", ".git/refs/heads/main", hookio.Reject},
		{"edit .git config rejects", "Edit", "/repo/.git/config", hookio.Reject},
		{"delete .git ref rejects", "Delete", "/repo/.git/refs/heads/main", hookio.Reject},
		{"edit non-git", "Edit", "/repo/src/main.go", hookio.Abstain},
		{"read gitignore", "Read", "/repo/.gitignore", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ti json.RawMessage
			if tt.tool == "Grep" || tt.tool == "Glob" {
				ti = searchJSON(tt.path)
			} else {
				ti = fileJSON(tt.path)
			}
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: ti}
			if got := r.Evaluate(input).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsGitMetadataPath(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{".git", true},
		{".git/", true},
		{".git/config", true},
		{"repo/.git/HEAD", true},
		{"foo/.git", true},
		{"/abs/repo/.git/objects", true},
		{"\"$r/.git/info/exclude\"", true},
		{"**/.git/**", true},
		{"", false},
		{"git", false},
		{".gitignore", false},
		{".gitmodules", false},
		{".git.bak", false},
		{"agit/x", false},
		{"my.git", false},
		{"the .git/index is 0 bytes", false},
		{"\"infra-block: repo .git/index is 0 bytes\"", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isGitMetadataPath(tt.in); got != tt.want {
				t.Errorf("isGitMetadataPath(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestHasInPlaceFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"bare -i", []string{"-i", "s/a/b/"}, true},
		{"-i with suffix", []string{"-i.bak", "s/a/b/"}, true},
		{"bundled -ri", []string{"-ri", "s/a/b/"}, true},
		{"long form", []string{"--in-place", "s/a/b/"}, true},
		{"no in-place", []string{"-n", "1p"}, false},
		{"-E only", []string{"-E", "s/a/b/"}, false},
		{"script after -e is not a flag scan", []string{"-e", "s/i/x/"}, false},
		{"bare dash", []string{"-"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasInPlaceFlag(tt.args); got != tt.want {
				t.Errorf("hasInPlaceFlag(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
