//go:build unix

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// The fake generator is this test binary re-executed: TestMain reads
// WR_FAKE_GEN=<mode> and behaves as a generator instead of running tests. It
// needs no separate build and works on macOS and Linux. Its registration MUST
// list WR_FAKE_GEN (and WR_FAKE_SCRIPT) in its env, because the environment
// is an allowlist.

const (
	envFakeGen    = "WR_FAKE_GEN"
	envFakeScript = "WR_FAKE_SCRIPT"
	envFakeFIFO   = "WR_FAKE_FIFO"
)

// script is the fake generator's instructions, JSON in WR_FAKE_SCRIPT. It
// performs its operations in the field order below.
type script struct {
	// IgnoreTerm ignores SIGTERM; ReadyFile is created once the generator is
	// set up (after the grandchild, if any, is running).
	IgnoreTerm bool   `json:"ignore_term,omitempty"`
	ReadyFile  string `json:"ready_file,omitempty"`
	// Dump writes {env, args, cwd, stdin} to this file in the cwd, reading
	// stdin to the end first.
	Dump string `json:"dump,omitempty"`

	Dirs      []string       `json:"dirs,omitempty"`
	Files     []scriptFile   `json:"files,omitempty"`
	Symlinks  []scriptLink   `json:"symlinks,omitempty"`
	Hardlinks []scriptLink   `json:"hardlinks,omitempty"`
	Sparse    []scriptSparse `json:"sparse,omitempty"`
	Stderr    string         `json:"stderr,omitempty"`
	StderrRep *scriptRepeat  `json:"stderr_repeat,omitempty"`
	Spawn     string         `json:"spawn,omitempty"` // "", "same_group", "hold_pipe"
	Hang      bool           `json:"hang,omitempty"`
	Stdout    string         `json:"stdout,omitempty"`
	StdoutLen int            `json:"stdout_len,omitempty"` // pad Stdout with spaces to this length
	Exit      int            `json:"exit,omitempty"`
}

type scriptFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type scriptLink struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

type scriptSparse struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type scriptRepeat struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

func TestMain(m *testing.M) {
	if mode := os.Getenv(envFakeGen); mode != "" {
		os.Exit(fakeMain(mode))
	}
	os.Exit(m.Run())
}

func fakeMain(mode string) int {
	switch mode {
	case "holder":
		// A grandchild: tell the test it is alive through the FIFO, then hold
		// the write end until killed.
		f, err := os.OpenFile(os.Getenv(envFakeFIFO), os.O_WRONLY, 0)
		if err != nil {
			return 90
		}
		_, _ = f.Write([]byte("x"))
		// Tell the generator that spawned this process that it is up.
		_, _ = os.NewFile(3, "up").Write([]byte("y"))
		blockForever()
		return 0
	case "script":
		var s script
		if err := json.Unmarshal([]byte(os.Getenv(envFakeScript)), &s); err != nil {
			fmt.Fprintln(os.Stderr, "fake: bad script:", err)
			return 91
		}
		return runScript(s)
	}
	fmt.Fprintln(os.Stderr, "fake: unknown mode", mode)
	return 92
}

func runScript(s script) int {
	if s.IgnoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	if s.Dump != "" {
		in, _ := io.ReadAll(os.Stdin)
		wd, _ := os.Getwd()
		b, _ := json.Marshal(map[string]any{"env": os.Environ(), "args": os.Args[1:], "cwd": wd, "stdin": string(in)})
		must(os.WriteFile(s.Dump, b, 0o600))
	}
	for _, d := range s.Dirs {
		must(os.MkdirAll(d, 0o755))
	}
	for _, f := range s.Files {
		must(os.MkdirAll(filepath.Dir(f.Path), 0o755))
		must(os.WriteFile(f.Path, []byte(f.Content), 0o644))
	}
	for _, l := range s.Symlinks {
		must(os.Symlink(l.Target, l.Path))
	}
	for _, l := range s.Hardlinks {
		must(os.Link(l.Target, l.Path))
	}
	for _, sp := range s.Sparse {
		f, err := os.Create(sp.Path)
		must(err)
		must(f.Truncate(sp.Size))
		must(f.Close())
	}
	if s.Stderr != "" {
		fmt.Fprint(os.Stderr, s.Stderr)
	}
	if r := s.StderrRep; r != nil {
		chunk := strings.Repeat(r.Text, 1000)
		for n := r.Count; n > 0; n -= 1000 {
			if n < 1000 {
				chunk = strings.Repeat(r.Text, n)
			}
			fmt.Fprint(os.Stderr, chunk)
		}
	}
	if s.Spawn != "" {
		cmd := exec.Command(os.Args[0])
		cmd.Env = []string{envFakeGen + "=holder", envFakeFIFO + "=" + os.Getenv(envFakeFIFO)}
		if s.Spawn == "hold_pipe" {
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		}
		up, upW, err := os.Pipe()
		must(err)
		cmd.ExtraFiles = []*os.File{upW}
		must(cmd.Start())
		upW.Close()
		// Wait until the grandchild is running and has reported through the
		// FIFO, so the group is never torn down before it existed.
		buf := make([]byte, 1)
		_, _ = io.ReadFull(up, buf)
		up.Close()
	}
	if s.ReadyFile != "" {
		must(os.WriteFile(s.ReadyFile, []byte("ready"), 0o600))
	}
	if s.Hang {
		blockForever()
	}
	out := s.Stdout
	if pad := s.StdoutLen - len(out); pad > 0 {
		out += strings.Repeat(" ", pad)
	}
	fmt.Fprint(os.Stdout, out)
	return s.Exit
}

// blockForever blocks without tripping the runtime deadlock detector (a timer
// is pending).
func blockForever() { time.Sleep(24 * time.Hour) }

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake:", err)
		os.Exit(93)
	}
}

// ---- test fixture ----

var fixedNow = time.Date(2026, 10, 20, 1, 28, 0, 0, time.UTC)

// seqReader yields a fixed byte sequence, repeating its last two bytes
// forever when exhausted.
type seqReader struct {
	mu    sync.Mutex
	data  []byte
	reads int
}

func (r *seqReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		switch {
		case len(r.data) >= 2 && r.reads*2+i < len(r.data):
			p[i] = r.data[r.reads*2+i]
		case len(r.data) >= 2:
			p[i] = r.data[len(r.data)-2+i%2]
		}
	}
	r.reads += (len(p) + 1) / 2
	return len(p), nil
}

type fixture struct {
	t      *testing.T
	runner *Runner
	parent string
	env    map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, parent: filepath.Join(t.TempDir(), "reports"), env: map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": "/home-for-test", "USER": "tester", "LANG": "C",
		"TMPDIR": os.TempDir(), "XDG_CONFIG_HOME": "/xdg/config", "XDG_STATE_HOME": "/xdg/state",
		"XDG_CACHE_HOME": "/xdg/cache", "AMBIENT_SECRET": "must-not-leak",
	}}
	f.runner = &Runner{
		ToolVersion: "test-version",
		Now:         func() time.Time { return fixedNow },
		Rand:        &seqReader{data: []byte{0xab, 0x12}},
		Grace:       100 * time.Millisecond,
		WaitDelay:   200 * time.Millisecond,
		LookupEnv:   func(k string) (string, bool) { v, ok := f.env[k]; return v, ok },
	}
	return f
}

// gen registers the fake generator with the given script. extraEnv names are
// added to its env list.
func (f *fixture) gen(s script, extraEnv ...string) config.Generator {
	f.t.Helper()
	exe, err := os.Executable()
	if err != nil {
		f.t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		f.t.Fatal(err)
	}
	f.env[envFakeGen] = "script"
	f.env[envFakeScript] = string(b)
	return config.Generator{
		Name:    "fake",
		Command: []string{exe},
		Timeout: 30 * time.Second,
		Env:     append([]string{envFakeGen, envFakeScript, envFakeFIFO}, extraEnv...),
	}
}

func testRange() Range {
	loc := time.FixedZone("EDT", -4*3600)
	return Range{
		Range: rangespec.Range{Since: time.Date(2026, 10, 12, 0, 0, 0, 0, loc), Before: time.Date(2026, 10, 19, 0, 0, 0, 0, loc)},
		Spec:  "last-week",
		Zone:  "America/New_York",
	}
}

func testEntries() []store.Entry {
	return []store.Entry{
		{
			ID: "e1", ExternalID: "x1", SourceID: "example-source", Type: "commit", OccurredAt: time.Date(2026, 10, 14, 0, 41, 12, 0, time.UTC),
			Summary: "first", URL: "https://example.invalid/1", Labels: []string{"kind:commit"}, Fields: json.RawMessage(`{"sha":"abc"}`),
		},
		{
			ID: "e2", ExternalID: "x2", SourceID: "example-source", Type: "note", OccurredAt: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC),
			Summary: "second",
		},
	}
}

func (f *fixture) request(g config.Generator) Request {
	return Request{
		Generator:    g,
		Preset:       "example-weekly",
		Range:        testRange(),
		Narrowing:    Narrowing{Labels: []string{"kind:commit"}},
		Entries:      testEntries(),
		Footer:       []FooterItem{{SourceID: "example-source", Count: 2, LastSuccessAt: time.Date(2026, 10, 19, 21, 2, 0, 0, time.UTC), LastOutcome: "succeeded"}},
		Config:       map[string]any{"model": "sonnet"},
		OutputParent: f.parent,
	}
}

func (f *fixture) run(g config.Generator) Outcome {
	f.t.Helper()
	return f.runner.Run(context.Background(), f.request(g))
}

// okManifest is the stdout of a well-formed manifest listing paths.
func okManifest(paths ...string) string {
	var files []map[string]any
	for _, p := range paths {
		files = append(files, map[string]any{"path": p, "content_type": "text/markdown"})
	}
	b, _ := json.Marshal(map[string]any{"contract_version": 1, "files": files, "summary": "ok", "warnings": []string{}})
	return string(b)
}

// okScript writes report.md and prints a manifest listing it.
func okScript() script {
	return script{
		Files:  []scriptFile{{Path: "report.md", Content: "# report\n"}},
		Stdout: okManifest("report.md"),
	}
}

func requireKind(t *testing.T, out Outcome, kind string) {
	t.Helper()
	if out.Record.OutcomeKind != kind {
		t.Fatalf("outcome_kind = %q (reason %q), want %q", out.Record.OutcomeKind, out.Record.Reason, kind)
	}
	wantStatus := StatusFailed
	if kind == KindOK {
		wantStatus = StatusOK
	}
	if out.Record.Status != wantStatus {
		t.Fatalf("status = %q, want %q", out.Record.Status, wantStatus)
	}
}

func requireOK(t *testing.T, out Outcome) {
	t.Helper()
	requireKind(t, out, KindOK)
}

// readManifest decodes manifest.json of a run directory into a generic map.
func readManifest(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest.json is not JSON: %v\n%s", err, b)
	}
	return m
}

// pollUntil retries cond until it holds or a generous deadline passes.
func pollUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// fifoWatcher watches a grandchild through a FIFO it writes one byte to and
// holds open: the byte proves it ran, and EOF proves it is gone (the kernel
// closes its end when the process dies, whether or not it was reaped).
type fifoWatcher struct {
	t    *testing.T
	f    *os.File
	path string
	seen bool
}

func newFIFOWatcher(t *testing.T, fx *fixture) *fifoWatcher {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	fx.env[envFakeFIFO] = path
	return &fifoWatcher{t: t, f: f, path: path}
}

// poll reads what is available and reports (sawByte, eof).
func (w *fifoWatcher) poll() (bool, bool) {
	buf := make([]byte, 16)
	for {
		n, err := w.f.Read(buf)
		if n > 0 {
			w.seen = true
			continue
		}
		if err == nil || errors.Is(err, io.EOF) {
			return w.seen, w.seen // EOF after the byte: all writers are gone
		}
		return w.seen, false // EAGAIN: a writer still holds the FIFO
	}
}

func (w *fifoWatcher) waitStarted() {
	w.t.Helper()
	pollUntil(w.t, "the grandchild to start", func() bool { s, _ := w.poll(); return s })
}

func (w *fifoWatcher) waitGone() {
	w.t.Helper()
	pollUntil(w.t, "the grandchild to be gone", func() bool { _, gone := w.poll(); return gone })
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	pollUntil(t, "file "+path, func() bool { _, err := os.Stat(path); return err == nil })
}

// runAsync starts Run on a cancelable context.
func (f *fixture) runAsync(g config.Generator) (cancel func(), result func() Outcome) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan Outcome, 1)
	go func() { ch <- f.runner.Run(ctx, f.request(g)) }()
	return cancel, func() Outcome { return <-ch }
}

func jsonMarshalString(s string) ([]byte, error) { return json.Marshal(s) }

// cycleReader hands out the given 2-byte suffixes in rotation.
type cycleReader struct {
	mu       sync.Mutex
	suffixes [][2]byte
	n        int
}

func (r *cycleReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.suffixes[r.n%len(r.suffixes)]
	r.n++
	return copy(p, s[:]), nil
}
