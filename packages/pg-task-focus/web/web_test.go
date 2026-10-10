package web

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The page's contract with the browser is in what it is allowed to load and
// what it is allowed to do, and the scans below are that contract as tests: a
// source file that logs to the console, stores to the browser, loads from
// another origin or makes a request outside the API client fails the build.
// The behavior they protect is INV-WEB-1 and INV-WEB-22 of
// docs/behavior/pg-task-focus/web-ui.md.

// Every file the page refers to is embedded, and it refers to nothing else.
func TestIndexLoadsOnlyItsOwnAssets(t *testing.T) {
	html := string(Index().Body)
	for _, bad := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)<style`),                         // an inline style element
		regexp.MustCompile(`(?i)\sstyle=`),                       // an inline style attribute
		regexp.MustCompile(`(?i)\son[a-z]+=`),                    // an inline event handler
		regexp.MustCompile(`(?i)(src|href|action)=["']?https?:`), // another origin
		regexp.MustCompile(`(?i)<(iframe|object|embed|form)\b`),
	} {
		if m := bad.FindString(html); m != "" {
			t.Errorf("index.html contains %q, which the page's content security policy forbids", m)
		}
	}
	for _, tag := range regexp.MustCompile(`(?i)<script\b[^>]*>`).FindAllString(html, -1) {
		if !strings.Contains(tag, "src=") {
			t.Errorf("index.html has an inline script %q, which the page's content security policy forbids", tag)
		}
	}
	refs := regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(refs) == 0 {
		t.Fatal("index.html loads no script or stylesheet")
	}
	for _, r := range refs {
		ref := r[1]
		if ref == "data:," {
			continue // the empty icon
		}
		name, ok := strings.CutPrefix(ref, "/assets/")
		if !ok {
			t.Errorf("index.html refers to %q; the page loads only from /assets/", ref)
			continue
		}
		if _, ok := Lookup(name); !ok {
			t.Errorf("index.html refers to /assets/%s, which is not embedded", name)
		}
	}
	if !regexp.MustCompile(`<script type="module" src="/assets/app\.mjs">`).MatchString(html) {
		t.Error("the page's one script is the module /assets/app.mjs")
	}
}

func TestEveryAssetHasItsContentTypeAndValidator(t *testing.T) {
	want := map[string]string{".mjs": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8"}
	names := Names()
	if len(names) < 10 {
		t.Fatalf("only %d assets are embedded: %v", len(names), names)
	}
	for _, n := range names {
		a, ok := Lookup(n)
		if !ok || len(a.Body) == 0 {
			t.Errorf("%s: not found or empty", n)
			continue
		}
		ct, known := want[filepath.Ext(n)]
		if !known {
			t.Errorf("%s: an asset with an extension the page cannot use", n)
			continue
		}
		if a.ContentType != ct {
			t.Errorf("%s: content type %q, want %q", n, a.ContentType, ct)
		}
		if !strings.HasPrefix(a.ETag, `"`) || !strings.HasSuffix(a.ETag, `"`) || len(a.ETag) < 10 {
			t.Errorf("%s: ETag %q is not a strong validator", n, a.ETag)
		}
	}
	if _, ok := Lookup("../go.mod"); ok {
		t.Error("a path outside the assets resolved")
	}
	if _, ok := Lookup("nope.mjs"); ok {
		t.Error("an unknown name resolved")
	}
}

// The scans: forbidden constructs by file, with a negative control that proves
// the scanner can fail.

type scan struct {
	why string
	re  *regexp.Regexp
}

var forbidden = []scan{
	{"it writes to the console, and free text the operator typed must never reach a log (INV-WEB-22)", regexp.MustCompile(`\bconsole\.`)},
	{"it uses browser storage, and nothing the operator typed is kept in the browser (INV-WEB-22)", regexp.MustCompile(`\b(localStorage|sessionStorage|indexedDB|document\.cookie|caches\.)`)},
	{"it makes a request that is not the API's (INV-WEB-22)", regexp.MustCompile(`\b(sendBeacon|XMLHttpRequest|WebSocket|importScripts|new\s+Worker|navigator\.serviceWorker)\b`)},
	{"it builds code or markup from text, which is how script injection starts", regexp.MustCompile(`\b(eval\s*\(|new\s+Function|innerHTML|outerHTML|insertAdjacentHTML|document\.write)`)},
	{"it loads a module at run time, which the page's policy and its tests cannot see", regexp.MustCompile(`\bimport\s*\(`)},
}

// stripComments removes whole-line and trailing // comments and /* */ blocks,
// so a sentence that mentions a forbidden word is not a use of it. It is
// deliberately simple: a // inside a string literal is not a comment, and the
// sources contain none that matter to a scan (URL literals are scanned
// separately, before comments are stripped, so they are checked either way).
func stripComments(src string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	src = block.ReplaceAllString(src, "")
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 && !strings.Contains(line[:i], `"`) && !strings.Contains(line[:i], "`") && !strings.Contains(line[:i], "'") {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func scanSource(name, src string) []string {
	var found []string
	code := stripComments(src)
	for _, f := range forbidden {
		if m := f.re.FindString(code); m != "" {
			found = append(found, name+": "+strconv.Quote(m)+": "+f.why)
		}
	}
	// A URL literal anywhere (even in a comment) would be an address the page
	// could reach; the page's addresses are all relative.
	if m := regexp.MustCompile(`https?://`).FindString(code); m != "" {
		found = append(found, name+": a URL literal "+strconv.Quote(m)+": the page reaches only its own origin (INV-WEB-1)")
	}
	return found
}

func TestPageSourceDoesNothingItMustNot(t *testing.T) {
	for _, n := range Names() {
		a, _ := Lookup(n)
		if strings.HasSuffix(n, ".css") {
			if regexp.MustCompile(`@import|url\(`).Match(a.Body) {
				t.Errorf("%s: a stylesheet that loads another file or another origin (it uses none)", n)
			}
			continue
		}
		for _, f := range scanSource(n, string(a.Body)) {
			t.Error(f)
		}
	}
}

func TestScannerCatchesWhatItIsFor(t *testing.T) {
	for src, want := range map[string]string{
		`console.log(reason);`:                      "console",
		`localStorage.setItem("reason", r)`:         "browser storage",
		`navigator.sendBeacon("/x", r)`:             "request that is not the API's",
		`el.innerHTML = note;`:                      "script injection",
		`const m = await import("./x.mjs");`:        "loads a module at run time",
		`const u = "https://example.test/collect";`: "URL literal",
		`new XMLHttpRequest()`:                      "request that is not the API's",
		`document.cookie = "x=" + reason`:           "browser storage",
	} {
		got := scanSource("probe.mjs", src)
		if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), want) {
			t.Errorf("the scan did not flag %q (%s): %v", src, want, got)
		}
	}
	for _, ok := range []string{
		"// we never call console.log here",
		"/* innerHTML is refused */ const a = 1;",
		"const message = 'the console is not used';",
	} {
		if got := scanSource("probe.mjs", ok); len(got) != 0 {
			t.Errorf("the scan flagged a harmless %q: %v", ok, got)
		}
	}
}

// The page makes its requests in one module and opens its stream in one.
func TestOnlyTheClientModulesMakeRequests(t *testing.T) {
	fetchCall := regexp.MustCompile(`\bfetch(Impl)?\s*\(`)
	stream := regexp.MustCompile(`new\s+EventSource\s*\(`)
	for _, n := range Names() {
		if !strings.HasSuffix(n, ".mjs") {
			continue
		}
		a, _ := Lookup(n)
		code := stripComments(string(a.Body))
		if fetchCall.MatchString(code) && n != "api.mjs" {
			t.Errorf("%s calls fetch; only api.mjs does, so every request carries the page's headers and goes to its own origin", n)
		}
		if stream.MatchString(code) && n != "stream.mjs" {
			t.Errorf("%s opens an event stream; only stream.mjs does", n)
		}
	}
	api, _ := Lookup("api.mjs")
	if !strings.Contains(string(api.Body), `"X-Client": "web"`) {
		t.Error("api.mjs does not name the client as web")
	}
	if regexp.MustCompile(`traceparent`).Match(api.Body) {
		t.Error("the browser has no tracing of its own; the daemon makes the trace id")
	}
}

// Every module the page imports is embedded, and imports are relative.
func TestImportsResolve(t *testing.T) {
	imp := regexp.MustCompile(`(?m)^\s*(?:import|export)\b[^;]*?\bfrom\s+"([^"]+)"`)
	for _, n := range Names() {
		if !strings.HasSuffix(n, ".mjs") {
			continue
		}
		a, _ := Lookup(n)
		for _, m := range imp.FindAllStringSubmatch(string(a.Body), -1) {
			spec := m[1]
			if !strings.HasPrefix(spec, "./") {
				t.Errorf("%s imports %q: only relative imports of sibling modules are allowed", n, spec)
				continue
			}
			if _, ok := Lookup(strings.TrimPrefix(spec, "./")); !ok {
				t.Errorf("%s imports %q, which is not embedded", n, spec)
			}
		}
	}
}

// ---- the client logic, under node ----

// requireNode is set by the Nix check, which supplies node; elsewhere a host
// without node skips the logic tests, with the reason, and says so.
const requireNode = "PG_TASK_FOCUS_REQUIRE_NODE"

func nodePath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv(requireNode) == "1" {
			t.Fatalf("node is not on PATH and %s=1: the client logic cannot go untested here", requireNode)
		}
		t.Skipf("node is not on PATH; the client logic tests need it (%s=1 makes this a failure)", requireNode)
	}
	return p
}

// NodeUnitTests runs every web/test/*.test.mjs under node's own test runner.
// There are no dependencies: the tests import the page's modules and a small
// fake DOM from the same tree.
func TestClientLogicUnderNode(t *testing.T) {
	node := nodePath(t)
	files, err := filepath.Glob(filepath.Join("test", "*.test.mjs"))
	if err != nil || len(files) < 5 {
		t.Fatalf("the client logic tests are missing: %v %v", files, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, append([]string{"--test"}, files...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, out.String())
	}
	if !regexp.MustCompile(`(?m)^ℹ fail 0$`).MatchString(out.String()) {
		t.Fatalf("node reported a failure or no summary:\n%s", out.String())
	}
	t.Log(lastLines(out.String(), 8))
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
