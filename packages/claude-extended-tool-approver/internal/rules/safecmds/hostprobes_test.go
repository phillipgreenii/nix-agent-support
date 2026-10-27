package safecmds

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// decideHost runs one command through a safecmds rule rooted at a synthetic project
// (/home/user/project: read-write; /tmp: read-write and a temp root; /etc unknown).
func decideHost(t *testing.T, cmd string) hookio.Decision {
	t.Helper()
	r := New(patheval.New("/home/user/project"))
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": cmd}),
	}
	return hookio.Verdict(r.Evaluate(input)).Decision
}

// TestSafecmds_HostScalars pins W4 (bead pg2-b1lir, ruling pg2-r9kv7): the host
// scalars a pool session reads (sysctl -n, pmset -g, uuidgen) are approved in their
// read forms and ONLY those.
func TestSafecmds_HostScalars(t *testing.T) {
	tests := []struct {
		cmd  string
		want hookio.Decision
	}{
		{"sysctl -n hw.ncpu", hookio.Approve},
		{"sysctl -n hw.ncpu hw.memsize", hookio.Approve},
		{"sysctl hw.ncpu", hookio.Approve},
		{"sysctl -n machdep.cpu.brand_string", hookio.Approve},
		{"sysctl -w kern.maxfiles=1", hookio.NoOpinion},
		{"sysctl kern.maxfiles=1", hookio.NoOpinion},
		{"sysctl -n kern.maxfiles=1", hookio.NoOpinion},
		{"sysctl -a", hookio.NoOpinion},
		{"sysctl -f /etc/sysctl.conf", hookio.NoOpinion},
		{"sysctl", hookio.NoOpinion},
		{"sysctl -n", hookio.NoOpinion},
		{"sysctl -n /etc/passwd", hookio.NoOpinion},
		{"pmset -g", hookio.Approve},
		{"pmset -g batt", hookio.Approve},
		{"pmset -g assertions", hookio.Approve},
		{"pmset -g log", hookio.Approve},
		{"pmset -g bogus", hookio.NoOpinion},
		{"pmset -g batt extra", hookio.NoOpinion},
		{"pmset -a sleep 0", hookio.NoOpinion},
		{"pmset sleepnow", hookio.NoOpinion},
		{"pmset displaysleepnow", hookio.NoOpinion},
		{"pmset", hookio.NoOpinion},
		{"uuidgen", hookio.Approve},
		{"uuidgen | tr A-Z a-z", hookio.Approve},
	}
	for _, tt := range tests {
		if got := decideHost(t, tt.cmd); got != tt.want {
			t.Errorf("%q: got %s, want %s", tt.cmd, got, tt.want)
		}
	}
}

// TestSafecmds_Mktemp pins the mktemp half of W6: only a template (or the default
// $TMPDIR) inside a writable temp root is approved.
func TestSafecmds_Mktemp(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp/pg2-b1lir-test-tmpdir")
	tests := []struct {
		cmd  string
		want hookio.Decision
	}{
		{"mktemp -d", hookio.Approve},
		{"mktemp", hookio.Approve},
		{"mktemp -d -t pg2x", hookio.Approve},
		{"mktemp -d /tmp/pg2-b1lir-XXXXXX", hookio.Approve},
		{"mktemp -d -p /tmp pg2x.XXXXXX", hookio.Approve},
		{"mktemp -d -q /tmp/a.XXXX /tmp/b.XXXX", hookio.Approve},
		{"mktemp -d /home/user/project/x.XXXXXX", hookio.NoOpinion},
		{"mktemp -d /etc/x.XXXXXX", hookio.NoOpinion},
		{"mktemp -d -p /home/user/project", hookio.NoOpinion},
		{"mktemp -d x.XXXXXX", hookio.NoOpinion}, // relative to the cwd (the project), not a temp root
		{"mktemp -d /tmp/a.XXXX /etc/b.XXXX", hookio.NoOpinion},
		{"mktemp --suffix=.x", hookio.NoOpinion},
		{"mktemp -d /tmp/../etc/x.XXXX", hookio.NoOpinion},
	}
	for _, tt := range tests {
		if got := decideHost(t, tt.cmd); got != tt.want {
			t.Errorf("%q: got %s, want %s", tt.cmd, got, tt.want)
		}
	}
}

// TestSafecmds_MktempNoTempDir proves the default location is only approved when
// $TMPDIR is itself a writable temp location: an unset TMPDIR falls back to /tmp.
func TestSafecmds_MktempNoTempDir(t *testing.T) {
	t.Setenv("TMPDIR", "")
	if got := decideHost(t, "mktemp -d"); got != hookio.Approve {
		t.Errorf("mktemp -d with TMPDIR unset: got %s, want approve (falls back to /tmp)", got)
	}
}

// TestSafecmds_Tar pins the tar half of W6: extraction of a readable archive into a
// writable temp root, and nothing else.
func TestSafecmds_Tar(t *testing.T) {
	tests := []struct {
		cmd  string
		want hookio.Decision
	}{
		{"tar -xf /tmp/pg2-b1lir/a.tar -C /tmp/pg2-b1lir/out", hookio.Approve},
		{"tar xzf /tmp/pg2-b1lir/a.tgz -C /tmp/pg2-b1lir/out", hookio.Approve},
		{"tar -xzvf /tmp/pg2-b1lir/a.tgz -C /tmp/out", hookio.Approve},
		{"tar -x -f /tmp/a.tar -C /tmp/out", hookio.Approve},
		{"tar --extract --file=/tmp/a.tar --directory=/tmp/out", hookio.Approve},
		{"tar --extract --file /tmp/a.tar --directory /tmp/out", hookio.Approve},
		{"tar -xf /home/user/project/a.tar -C /tmp/out", hookio.Approve},
		{"tar -xf /tmp/a.tar -C /tmp/out sub/member.txt", hookio.Approve},
		// not an extraction
		{"tar -cf /tmp/a.tar x", hookio.NoOpinion},
		{"tar -tf /tmp/a.tar", hookio.NoOpinion},
		{"tar -rf /tmp/a.tar x", hookio.NoOpinion},
		// destination not a writable temp root
		{"tar -xf /tmp/a.tar -C /home/user/project/out", hookio.NoOpinion},
		{"tar -xf /tmp/a.tar -C /etc", hookio.NoOpinion},
		{"tar -xf /tmp/a.tar", hookio.NoOpinion}, // no -C: the cwd (the project) is not a temp root
		// archive not readable / missing / stdin
		{"tar -xf /etc/a.tar -C /tmp/out", hookio.NoOpinion},
		{"tar -xf", hookio.NoOpinion},
		{"tar -xf - -C /tmp/out", hookio.NoOpinion},
		{"tar -x -C /tmp/out", hookio.NoOpinion},
		// switches that remove tar's own protections or run a program
		{"tar -xPf /tmp/a.tar -C /tmp/out", hookio.NoOpinion},
		{"tar -xf /tmp/a.tar -C /tmp/out --absolute-names", hookio.NoOpinion},
		{"tar -xf /tmp/a.tar -C /tmp/out --to-command=sh", hookio.NoOpinion},
		{"tar -xf /tmp/a.tar -C /tmp/out --use-compress-program=sh", hookio.NoOpinion},
		{"tar -xf /tmp/a.tar -C /tmp/out --checkpoint-action=exec=sh", hookio.NoOpinion},
		{"tar -xIf sh /tmp/a.tar -C /tmp/out", hookio.NoOpinion},
		// member operands that escape
		{"tar -xf /tmp/a.tar -C /tmp/out /etc/passwd", hookio.NoOpinion},
		{"tar -xf /tmp/a.tar -C /tmp/out ../evil", hookio.NoOpinion},
		{"tar -xf /tmp/a.tar -C /tmp/out a/../../evil", hookio.NoOpinion},
		// two archives, or a relative archive that a preceding -C would re-base
		{"tar -xf /tmp/a.tar -f /tmp/b.tar -C /tmp/out", hookio.NoOpinion},
		{"tar -C /tmp/out -xf a.tar", hookio.NoOpinion},
	}
	for _, tt := range tests {
		if got := decideHost(t, tt.cmd); got != tt.want {
			t.Errorf("%q: got %s, want %s", tt.cmd, got, tt.want)
		}
	}
}
