package prepare

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
)

const genericQueries = `{"mine":"is:pr is:open author:@me","team":["is:pr is:open review-requested:@me","is:pr is:open assignee:@me"]}`

// stubDaemon installs a stand-in pg-connector-github the way the nix package
// does: a wrapper script plus its unwrapped .<name>-wrapped sibling, which
// prints a version line for --version.
func stubDaemon(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pg-connector-github"), []byte("#!/bin/sh\nexec \"$0-wrapped\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\ncase \"$1\" in --version) echo 'pg-connector-github 9.9.9'; echo second-line;; esac\n"
	if err := os.WriteFile(filepath.Join(dir, ".pg-connector-github-wrapped"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// shortRoot is a scratch root short enough for the 103-byte socket limit (the
// default macOS test temp directory is not).
func shortRoot(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return safety.Resolve(d)
}

func writeQueries(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "queries.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func daemonOptions(t *testing.T, root string) Options {
	t.Helper()
	return Options{
		Root: root, Home: t.TempDir(), Self: "/bin/true", Queries: []string{"mine", "team"},
		ToolDirs: []string{stubDaemon(t)}, DaemonQueriesFile: writeQueries(t, genericQueries),
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	}
}

func TestDefaultsDaemonCap(t *testing.T) {
	o := daemonOptions(t, t.TempDir())
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.DaemonPointsPerHour != 1500 {
		t.Errorf("default cap = %d, want 1500", o.DaemonPointsPerHour)
	}
	for n, wantErr := range map[int]bool{1: false, 2500: false, 100000: false, 100001: true, -5: true} {
		o := daemonOptions(t, t.TempDir())
		o.DaemonPointsPerHour = n
		err := o.Defaults()
		if (err != nil) != wantErr {
			t.Errorf("cap %d: err = %v, want error %v", n, err, wantErr)
		}
		if err == nil && o.DaemonPointsPerHour != n {
			t.Errorf("cap %d became %d", n, o.DaemonPointsPerHour)
		}
	}
}

func TestResolveDaemonPinsTheUnwrappedBinaryAndRendersTheConfig(t *testing.T) {
	root := shortRoot(t)
	o := daemonOptions(t, root)
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	l := scratch.Layout{Root: root}
	d, err := resolveDaemon(o, l)
	if err != nil {
		t.Fatal(err)
	}
	if d.tool.Name != "pg-connector-github" || !strings.HasSuffix(d.tool.Path, ".pg-connector-github-wrapped") || d.tool.Wrapper == "" {
		t.Errorf("tool = %+v, want the unwrapped sibling", d.tool)
	}
	var doc struct {
		StateDir string `json:"state_dir"`
		Socket   string `json:"socket_path"`
		GH       string `json:"gh_path"`
		Points   int    `json:"graphql_points_per_hour"`
		PR       struct {
			Queries map[string]any `json:"queries"`
		} `json:"pr"`
	}
	if err := json.Unmarshal(d.config, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.StateDir != l.DaemonStateDir() || doc.Socket != l.DaemonSocket() || doc.GH != filepath.Join(l.BinDir(), "gh") || doc.Points != 1500 || len(doc.PR.Queries) != 2 {
		t.Errorf("config = %+v", doc)
	}
	// The pinned binary reports its build exactly like the other tools.
	link := filepath.Join(t.TempDir(), "pg-connector-github")
	if err := os.Symlink(d.tool.Path, link); err != nil {
		t.Fatal(err)
	}
	if got := scratch.Version(context.Background(), link); got != "pg-connector-github 9.9.9" {
		t.Errorf("Version = %q", got)
	}
}

// Every refusal happens before anything is written: no manifest, no tree.
func TestPrepareRefusesBeforeWritingAnything(t *testing.T) {
	cases := map[string]struct {
		mutate func(t *testing.T, o *Options)
		want   []string
		never  string
	}{
		"binary missing":          {func(t *testing.T, o *Options) { o.ToolDirs = nil }, []string{"pg-connector-github"}, ""},
		"no queries file flag":    {func(t *testing.T, o *Options) { o.DaemonQueriesFile = "" }, []string{"--daemon-queries-file"}, ""},
		"queries file unreadable": {func(t *testing.T, o *Options) { o.DaemonQueriesFile = filepath.Join(t.TempDir(), "absent.json") }, []string{"daemon queries file"}, ""},
		"watched query absent": {func(t *testing.T, o *Options) {
			o.DaemonQueriesFile = writeQueries(t, `{"mine":"is:pr hidden-expression"}`)
		}, []string{`"team"`}, "hidden-expression"},
		"query value empty": {func(t *testing.T, o *Options) {
			o.DaemonQueriesFile = writeQueries(t, `{"mine":"x","team":[]}`)
		}, []string{`"team"`}, ""},
		"invalid json": {func(t *testing.T, o *Options) { o.DaemonQueriesFile = writeQueries(t, `{`) }, []string{"JSON"}, ""},
	}
	for name, tc := range cases {
		root := filepath.Join(safety.Resolve(t.TempDir()), "scratch")
		o := daemonOptions(t, root)
		tc.mutate(t, &o)
		l, _, err := Prepare(context.Background(), o)
		if err == nil {
			t.Errorf("%s: Prepare succeeded", name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q lacks %q", name, err, w)
			}
		}
		if tc.never != "" && strings.Contains(err.Error(), tc.never) {
			t.Errorf("%s: error leaks an expression: %v", name, err)
		}
		if _, statErr := os.Stat(l.Manifest()); statErr == nil {
			t.Errorf("%s: a manifest was written", name)
		}
		if _, statErr := os.Stat(root); statErr == nil {
			t.Errorf("%s: the scratch directory was created before the refusal", name)
		}
	}
}

func TestPrepareRefusesAScratchDirectoryWhoseSocketPathIsTooLong(t *testing.T) {
	base := safety.Resolve(t.TempDir())
	root := filepath.Join(base, strings.Repeat("d", scratch.MaxSocketPathBytes))
	o := daemonOptions(t, root)
	l, _, err := Prepare(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "socket") || !strings.Contains(err.Error(), "103") {
		t.Fatalf("err = %v, want a socket path limit refusal", err)
	}
	if _, statErr := os.Stat(l.Manifest()); statErr == nil {
		t.Error("a manifest was written")
	}
}
