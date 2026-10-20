//go:build unix

package runner

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
)

var runIDRE = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{4}$`)

func builtinReq(fx *fixture) Request {
	req := fx.request(config.Generator{Name: "baseline", Builtin: true})
	req.Builtin = func(_ context.Context, _ Envelope, dir string) (GeneratorManifest, error) {
		return GeneratorManifest{Files: []ManifestFile{{Path: "r.md", ContentType: "text/markdown"}}},
			os.WriteFile(filepath.Join(dir, "r.md"), []byte("x"), 0o600)
	}
	return req
}

func TestRunDirectoryNameUsesTheInjectedClockAndRandomSource(t *testing.T) {
	fx := newFixture(t)
	out := fx.runner.Run(context.Background(), builtinReq(fx))
	requireOK(t, out)
	if got := filepath.Base(out.RunDir); got != "20261020T012800Z-ab12" || !runIDRE.MatchString(got) {
		t.Fatalf("run dir = %q", got)
	}
	if filepath.Dir(out.RunDir) != fx.parent {
		t.Fatalf("run dir %q is not a child of %q", out.RunDir, fx.parent)
	}
}

func TestCollisionRetriesWithANewSuffix(t *testing.T) {
	fx := newFixture(t)
	// Same second; the first attempt of the second run collides with the
	// first run's directory, the next suffix is free.
	fx.runner.Rand = &seqReader{data: []byte{0xab, 0x12, 0xab, 0x12, 0xcd, 0x34}}
	first := fx.runner.Run(context.Background(), builtinReq(fx))
	second := fx.runner.Run(context.Background(), builtinReq(fx))
	requireOK(t, first)
	requireOK(t, second)
	if filepath.Base(first.RunDir) != "20261020T012800Z-ab12" || filepath.Base(second.RunDir) != "20261020T012800Z-cd34" {
		t.Fatalf("run dirs = %q, %q", first.RunDir, second.RunDir)
	}
}

func TestForcedIdenticalSuffixStopsAfterTenAttempts(t *testing.T) {
	fx := newFixture(t)
	rd := &seqReader{data: []byte{0xab, 0x12}} // every attempt yields the same suffix
	fx.runner.Rand = rd
	requireOK(t, fx.runner.Run(context.Background(), builtinReq(fx)))
	before := rd.reads
	out := fx.runner.Run(context.Background(), builtinReq(fx))
	requireKind(t, out, KindOutputDirUnwritable)
	if out.RunDir != "" {
		t.Fatalf("run dir = %q", out.RunDir)
	}
	if attempts := rd.reads - before; attempts != MaxCollisionRetries {
		t.Fatalf("made %d attempts, want %d", attempts, MaxCollisionRetries)
	}
	if !strings.Contains(out.Record.Reason, "10 attempts") {
		t.Fatalf("reason = %q", out.Record.Reason)
	}
}

func TestMissingParentIsCreatedOwnerOnly(t *testing.T) {
	fx := newFixture(t)
	fx.parent = filepath.Join(t.TempDir(), "a", "b", "reports")
	out := fx.runner.Run(context.Background(), builtinReq(fx))
	requireOK(t, out)
	for _, dir := range []string{fx.parent, filepath.Dir(fx.parent)} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %o, want 0700", dir, fi.Mode().Perm())
		}
	}
}

func TestExistingParentModeIsLeftAlone(t *testing.T) {
	fx := newFixture(t)
	if err := os.MkdirAll(fx.parent, 0o755); err != nil {
		t.Fatal(err)
	}
	requireOK(t, fx.runner.Run(context.Background(), builtinReq(fx)))
	fi, _ := os.Stat(fx.parent)
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("an existing parent was changed to %o", fi.Mode().Perm())
	}
}

func TestUnwritableParentIsOutputDirUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	fx := newFixture(t)
	if err := os.MkdirAll(fx.parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fx.parent, 0o700) })
	out := fx.runner.Run(context.Background(), builtinReq(fx))
	requireKind(t, out, KindOutputDirUnwritable)
	if out.RunDir != "" || out.Record.RunID == "" || out.Record.Reason == "" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRelativeParentIsRejected(t *testing.T) {
	fx := newFixture(t)
	req := builtinReq(fx)
	req.OutputParent = "relative/reports"
	out := fx.runner.Run(context.Background(), req)
	requireKind(t, out, KindOutputDirUnwritable)
	if _, err := os.Stat("relative"); err == nil {
		t.Fatal("a relative directory was created")
	}
}

func TestLatestSymlinkFollowsEachSuccessfulRun(t *testing.T) {
	fx := newFixture(t)
	fx.runner.Rand = &seqReader{data: []byte{0xab, 0x12, 0xcd, 0x34}}
	first := fx.runner.Run(context.Background(), builtinReq(fx))
	requireOK(t, first)
	link := filepath.Join(fx.parent, LatestLink)
	if got, err := os.Readlink(link); err != nil || got != filepath.Base(first.RunDir) {
		t.Fatalf("latest = %q (%v), want %q", got, err, filepath.Base(first.RunDir))
	}
	second := fx.runner.Run(context.Background(), builtinReq(fx))
	requireOK(t, second)
	if got, _ := os.Readlink(link); got != filepath.Base(second.RunDir) {
		t.Fatalf("latest = %q, want %q", got, filepath.Base(second.RunDir))
	}
	if len(first.Notices)+len(second.Notices) != 0 {
		t.Fatalf("notices = %v %v", first.Notices, second.Notices)
	}
	entries, _ := os.ReadDir(fx.parent)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary symlink %q left behind", e.Name())
		}
	}
	// latest points through to the run's files.
	if _, err := os.Stat(filepath.Join(link, "r.md")); err != nil {
		t.Fatal(err)
	}
}

func TestLatestThatIsNotASymlinkIsLeftAloneWithAWarning(t *testing.T) {
	for _, mk := range map[string]func(string) error{
		"file":      func(p string) error { return os.WriteFile(p, []byte("mine"), 0o600) },
		"directory": func(p string) error { return os.Mkdir(p, 0o700) },
	} {
		fx := newFixture(t)
		if err := os.MkdirAll(fx.parent, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(fx.parent, LatestLink)
		if err := mk(link); err != nil {
			t.Fatal(err)
		}
		out := fx.runner.Run(context.Background(), builtinReq(fx))
		requireOK(t, out)
		if len(out.Notices) != 1 || !strings.Contains(out.Notices[0], "not a symlink") {
			t.Fatalf("notices = %v", out.Notices)
		}
		fi, err := os.Lstat(link)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("latest was replaced: %v %v", fi, err)
		}
	}
}

func TestConcurrentRunsGetDistinctDirectoriesUnderACollidingSuffix(t *testing.T) {
	// The random source only ever offers two suffixes, so concurrent runs in
	// the same second must collide and retry; all still end up distinct.
	fx := newFixture(t)
	fx.runner.Rand = &cycleReader{suffixes: [][2]byte{{0xaa, 0x01}, {0xaa, 0x02}, {0xaa, 0x03}, {0xaa, 0x04}, {0xaa, 0x05}, {0xaa, 0x06}}}
	const n = 5
	res := make(chan Outcome, n)
	for i := 0; i < n; i++ {
		go func() { res <- fx.runner.Run(context.Background(), builtinReq(fx)) }()
	}
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		out := <-res
		requireOK(t, out)
		if seen[out.RunDir] {
			t.Fatalf("two runs share %q", out.RunDir)
		}
		seen[out.RunDir] = true
	}
}
