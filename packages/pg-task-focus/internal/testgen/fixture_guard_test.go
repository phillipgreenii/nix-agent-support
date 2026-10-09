package testgen

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The repository is public, so every fixture is synthetic: the only host a
// fixture may name is example.test (or a subdomain of it; a URL with no host
// names none), no fixture holds
// an address or any other text with an "@", and every task link points at
// https://example.test/.

// moduleRoot is the root of the pg-task-focus module, seen from this package.
const moduleRoot = "../.."

// syntheticHost is the one host fixtures may name, with its subdomains.
const syntheticHost = "example.test"

// linkPrefix is what every link value in a fixture starts with.
const linkPrefix = "https://example.test/"

var (
	urlPattern  = regexp.MustCompile(`(?i)\bhttps?://([^/\s"'\\?#]*)`)
	linkPattern = regexp.MustCompile(`"link"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// fixtureProblems lists every way content breaks the synthetic-fixture rule,
// one sentence each; none means the content is synthetic.
func fixtureProblems(content string) []string {
	var out []string
	if strings.Contains(content, "@") {
		out = append(out, `it contains "@"`)
	}
	for _, m := range urlPattern.FindAllStringSubmatch(content, -1) {
		host := strings.ToLower(m[1])
		if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[:i]
		}
		// A URL with no host (an invalid configuration's fixture) names none.
		if host != "" && host != syntheticHost && !strings.HasSuffix(host, "."+syntheticHost) {
			out = append(out, "the URL "+m[0]+" names a host other than "+syntheticHost)
		}
	}
	for _, m := range linkPattern.FindAllStringSubmatch(content, -1) {
		if !strings.HasPrefix(m[1], linkPrefix) {
			out = append(out, "the link "+m[1]+" does not start with "+linkPrefix)
		}
	}
	return out
}

// fixtureFiles lists every regular file under a testdata directory of the
// module, as slash paths relative to the module root.
func fixtureFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if slices.Contains(strings.Split(rel, "/"), "testdata") {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	return out
}

func TestFixturesAreSynthetic(t *testing.T) {
	files := fixtureFiles(t)
	// The walk must reach the fixtures it guards, or the test proves nothing.
	for _, want := range []string{"testdata/config/valid.json", "testdata/logs/golden/normal-day.jsonl"} {
		if !slices.Contains(files, want) {
			t.Fatalf("the walk did not reach %s (found %d files)", want, len(files))
		}
	}
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range fixtureProblems(string(raw)) {
			t.Errorf("%s: %s", rel, p)
		}
	}
}

// TestFixtureProblemsFindsEachViolation pins the checker itself, so that a
// guard that silently accepts everything cannot pass.
func TestFixtureProblemsFindsEachViolation(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"a synthetic event", `{"link":"https://example.test/tasks/plan","kv":[{"key":"pr","value":"https://example.test/pr/1"}]}`, 0},
		{"a subdomain of the synthetic host", `{"public_url":"https://focus.example.test"}`, 0},
		{"a synthetic host with a port", `see http://example.test:8080/x`, 0},
		{"plain text with no URL", `Posted in the standup instead`, 0},
		{"a URL with no host", `{"public_url":"https://:8080"}`, 0},
		{"an at sign", `{"note":"ask someone` + "\x40" + `somewhere"}`, 1},
		{"another host", `{"value":"https://elsewhere.invalid/pr/1"}`, 1},
		{"a host that only ends like the synthetic one", `{"value":"https://notexample.test/x"}`, 1},
		{"a host in capitals", `{"value":"HTTPS://ELSEWHERE.INVALID/x"}`, 1},
		{"a link on a subdomain", `{"link":"https://tasks.example.test/plan"}`, 1},
		{"a link that is not a URL", `{"link":"plan"}`, 1},
		{"an empty link", `{"link":""}`, 1},
		{"a link with an escaped quote", `{"link":"https://example.test/a\"b"}`, 0},
		{"a link on another host", `{"link": "https://elsewhere.invalid/x"}`, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fixtureProblems(c.content); len(got) != c.want {
				t.Errorf("fixtureProblems(%q) = %q, want %d problems", c.content, got, c.want)
			}
		})
	}
}
