package daemon_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/web"
)

// The web UI's assets are served from the binary, through the same defences as
// every route (INV-WEB-1): the Host allowlist, a strict content security policy,
// a content type of their own and a validator, and a problem for any name that
// is not one of them.

func TestWebUIAssetsAreServedFromTheBinary(t *testing.T) {
	e := newEnv(t, options{})
	names := web.Names()
	if len(names) < 10 {
		t.Fatalf("only %d assets: %v", len(names), names)
	}
	policy := []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "base-uri 'none'", "form-action 'none'", "frame-ancestors 'none'"}
	for _, n := range append([]string{""}, names...) {
		path, want := "/assets/"+n, web.Index()
		if n == "" {
			path = "/"
		} else {
			a, _ := web.Lookup(n)
			want = a
		}
		r := e.raw("GET", path, nil, nil)
		if r.Status != 200 {
			t.Errorf("GET %s: %d %s", path, r.Status, r.Body)
			continue
		}
		if got := r.Header.Get("Content-Type"); got != want.ContentType {
			t.Errorf("GET %s: content type %q, want %q", path, got, want.ContentType)
		}
		if string(r.Body) != string(want.Body) {
			t.Errorf("GET %s: the body is not the embedded file", path)
		}
		csp := r.Header.Get("Content-Security-Policy")
		for _, d := range policy {
			if !strings.Contains(csp, d) {
				t.Errorf("GET %s: the policy %q lacks %q", path, csp, d)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "*") {
			t.Errorf("GET %s: the policy allows inline or arbitrary code: %q", path, csp)
		}
		for h, v := range map[string]string{"X-Content-Type-Options": "nosniff", "Cache-Control": "no-cache", "Referrer-Policy": "no-referrer", "ETag": want.ETag} {
			if got := r.Header.Get(h); got != v {
				t.Errorf("GET %s: %s = %q, want %q", path, h, got, v)
			}
		}
		for h := range r.Header {
			if strings.HasPrefix(strings.ToLower(h), "access-control-") {
				t.Errorf("GET %s: sends a CORS header %s", path, h)
			}
		}
		// A page that has not changed is a conditional request.
		if c := e.raw("GET", path, nil, map[string]string{"If-None-Match": want.ETag}); c.Status != 304 || len(c.Body) != 0 {
			t.Errorf("GET %s with its validator: %d with %d bytes, want 304 with none", path, c.Status, len(c.Body))
		}
		if c := e.raw("GET", path, nil, map[string]string{"If-None-Match": `"other", ` + want.ETag}); c.Status != 304 {
			t.Errorf("GET %s with a list of validators that holds its own: %d", path, c.Status)
		}
		if c := e.raw("GET", path, nil, map[string]string{"If-None-Match": `"other"`}); c.Status != 200 {
			t.Errorf("GET %s with another validator: %d", path, c.Status)
		}
	}
}

func TestWebUIAssetsAreBehindTheDefences(t *testing.T) {
	e := newEnv(t, options{})
	for _, path := range []string{"/", "/assets/app.mjs", "/assets/style.css"} {
		if r := e.raw("GET", path, nil, map[string]string{"Host": "evil.example"}); r.Status != 403 || r.Reason() != "forbidden_host" {
			t.Errorf("GET %s with a foreign Host: %d %q", path, r.Status, r.Reason())
		}
		if r := e.raw("GET", path, nil, map[string]string{"Origin": "https://evil.example"}); r.Status != 403 || r.Reason() != "forbidden_origin" {
			t.Errorf("GET %s with a foreign Origin: %d %q", path, r.Status, r.Reason())
		}
	}
	// The page is also served under the configured public URL's host.
	if r := e.raw("GET", "/assets/app.mjs", nil, map[string]string{"Host": "focus.example.test"}); r.Status != 200 {
		t.Errorf("the public URL's host: %d", r.Status)
	}
	if r := e.raw("POST", "/assets/app.mjs", []byte(`{}`), map[string]string{"Content-Type": "application/json"}); r.Status != 405 || r.Reason() != "method_not_allowed" {
		t.Errorf("an asset takes only GET: %d %q", r.Status, r.Reason())
	}
}

// A name that is not one of the embedded files is the not_found problem of any
// unknown path: there is no file system behind the route to traverse.
func TestWebUIUnknownAssetNamesAreProblems(t *testing.T) {
	e := newEnv(t, options{})
	for _, path := range []string{
		"/assets/nope.mjs", "/assets/..%2Fgo.mod", "/assets/%2e%2e%2fgo.mod", "/assets/", "/assets/views/x.mjs",
		"/assets/app.mjs/", "/assets/APP.MJS", "/assets/index.html", "/assets/embed.go", "/assets/%00", "/assets/app.mjs%00.css",
	} {
		r := e.raw("GET", path, nil, nil)
		if r.Status != 404 && r.Status != 400 {
			t.Errorf("GET %s: %d, want a refusal", path, r.Status)
			continue
		}
		if r.Status == 404 {
			if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
				t.Errorf("GET %s: a refusal must be a problem document, got %q", path, ct)
			}
			e.spec.ValidateResponse(t, "GET", "/assets/{name}", r.Status, r.Header.Get("Content-Type"), r.Body)
		}
	}
}

// The route's metric label is its template, so no probe makes a label, and
// the access log never names the asset or the page's content.
func TestWebUIRequestsAreCountedByTemplateAndNeverLogged(t *testing.T) {
	e := newEnv(t, options{})
	e.raw("GET", "/assets/app.mjs", nil, nil)
	e.raw("GET", "/assets/style.css", nil, nil)
	e.raw("GET", "/assets/not-an-asset-"+strings.Repeat("x", 40), nil, nil)
	f := e.scrape()["pg_task_focus_http_requests_total"]
	if v, _ := sampleValue(f, map[string]string{"route": "/assets/{name}", "status": "200"}); v != 2 {
		t.Errorf("the asset route is counted by its template: %v", v)
	}
	if v, _ := sampleValue(f, map[string]string{"route": "/assets/{name}", "status": "404"}); v != 1 {
		t.Errorf("an unknown asset is a 404 on the same template: %v", v)
	}
	for _, m := range f.Metric {
		for _, l := range m.Label {
			if strings.Contains(l.GetValue(), "not-an-asset") || strings.Contains(l.GetValue(), "app.mjs") {
				t.Errorf("a metric label names an asset: %s=%s", l.GetName(), l.GetValue())
			}
		}
	}
	if log := e.log.String(); strings.Contains(log, "not-an-asset") || strings.Contains(log, "app.mjs") {
		t.Errorf("the log names a requested asset:\n%s", log)
	}
}

// The assets directory the binary embeds is the directory under web/.
func TestEmbeddedAssetsAreTheFilesOnDisk(t *testing.T) {
	files, err := filepath.Glob("../../web/assets/*")
	if err != nil || len(files) != len(web.Names()) {
		t.Fatalf("web/assets has %d files and the binary embeds %d: %v", len(files), len(web.Names()), err)
	}
}
