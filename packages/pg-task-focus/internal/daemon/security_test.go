package daemon_test

import (
	"fmt"
	"strings"
	"testing"
)

// The Host and Origin matrix of the browser-exposure rules, on every route.
func TestHostAndOriginMatrix(t *testing.T) {
	e := newEnv(t, options{})
	port := e.port
	local127 := fmt.Sprintf("127.0.0.1:%d", port)
	localhost := fmt.Sprintf("localhost:%d", port)

	routes := []string{"/api/v1/state", "/api/v1/stream", "/api/v1/config", "/healthz", "/readyz", "/metrics"}
	for _, path := range routes {
		for host, want := range map[string]int{
			local127:                             200,
			localhost:                            200,
			"focus.example.test":                 200, // the configured public URL's host
			"evil.example":                       403, // DNS rebinding
			"localhost":                          403, // the port is part of the allowlist entry
			"127.0.0.1":                          403,
			fmt.Sprintf("evil.example:%d", port): 403,
		} {
			if path == "/api/v1/stream" && want == 200 {
				continue // a stream stays open; its Host check is the same code path, covered by the 403 rows
			}
			r := e.raw("GET", path, nil, map[string]string{"Host": host})
			if r.Status != want {
				t.Errorf("GET %s with Host %q: %d, want %d", path, host, r.Status, want)
			}
			if want == 403 && r.Reason() != "forbidden_host" {
				t.Errorf("GET %s with Host %q: reason %q, want forbidden_host", path, host, r.Reason())
			}
		}
	}

	for origin, want := range map[string]int{
		"":                           200, // absent: machine clients and same-origin reads
		"null":                       403,
		"https://focus.example.test": 200,
		"http://" + localhost:        200,
		"http://" + local127:         200,
		"https://evil.example":       403,
		"http://focus.example.test":  403, // wrong scheme for the configured URL
		"http://localhost":           403,
	} {
		hdr := map[string]string{}
		if origin != "" {
			hdr["Origin"] = origin
		}
		r := e.raw("GET", "/api/v1/state", nil, hdr)
		if r.Status != want {
			t.Errorf("Origin %q: %d, want %d", origin, r.Status, want)
		}
		if want == 403 && r.Reason() != "forbidden_origin" {
			t.Errorf("Origin %q: reason %q", origin, r.Reason())
		}
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("a response carries the CORS header %s", k)
			}
		}
	}
}

func TestContentTypeBodyAndMethodRules(t *testing.T) {
	e := newEnv(t, options{})
	body := []byte(`{}`)
	for ct, want := range map[string]int{
		"":                                  415,
		"text/plain":                        415,
		"application/x-www-form-urlencoded": 415,
		"application/json":                  400, // accepted as a type; {} is an incomplete change
		"application/json; charset=utf-8":   400,
		"APPLICATION/JSON":                  400,
	} {
		hdr := map[string]string{}
		if ct != "" {
			hdr["Content-Type"] = ct
		}
		r := e.raw("POST", "/api/v1/periods/change", body, hdr)
		if r.Status != want {
			t.Errorf("Content-Type %q: %d, want %d (%s)", ct, r.Status, want, r.Body)
		}
	}

	json := map[string]string{"Content-Type": "application/json"}
	for name, b := range map[string]string{
		"invalid UTF-8":    "{\"changes\":[],\"profile\":\"\xff\xfe\"}",
		"a duplicate key":  `{"changes":[],"changes":[]}`,
		"an unknown field": `{"changes":[],"nonsense":1}`,
		"trailing data":    `{"changes":[]} {"x":1}`,
		"an empty body":    ``,
		"not an object":    `[1,2]`,
		"a wrong type":     `{"changes":"day"}`,
	} {
		r := e.raw("POST", "/api/v1/periods/change", []byte(b), json)
		if r.Status != 400 || r.Reason() != "invalid_request" {
			t.Errorf("%s: %d %q, want 400 invalid_request (%s)", name, r.Status, r.Reason(), r.Body)
		}
	}

	if r := e.raw("OPTIONS", "/api/v1/state", nil, nil); r.Status != 405 || r.Reason() != "method_not_allowed" {
		t.Errorf("OPTIONS: %d %q, want 405 method_not_allowed (no CORS preflight is served)", r.Status, r.Reason())
	}
	if r := e.raw("DELETE", "/api/v1/periods/change", nil, json); r.Status != 405 {
		t.Errorf("DELETE: %d", r.Status)
	}
	if r := e.raw("GET", "/api/v1/nowhere", nil, nil); r.Status != 404 || r.Reason() != "not_found" {
		t.Errorf("an unknown path: %d %q", r.Status, r.Reason())
	}
	// Every refusal of the transport is a problem document that validates.
	r := e.raw("GET", "/api/v1/state", nil, map[string]string{"Host": "evil.example"})
	e.spec.ValidateResponse(t, "GET", "/api/v1/state", r.Status, r.Header.Get("Content-Type"), r.Body)
	if r.Header.Get("traceresponse") == "" {
		t.Errorf("a problem carries no traceresponse header")
	}
}
