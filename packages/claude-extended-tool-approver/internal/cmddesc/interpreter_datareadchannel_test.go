package cmddesc

import (
	"reflect"
	"testing"
)

// TestDataReadChannel covers the four data-read channels (Phase 2f,
// tc-o14i5.3.6): curl @file / file://, yq --from-file / load(), jq -L, and
// date -f each produce an EffectPath read naming the right path argument.
// Judgment (secret/protected path) is the existing path-read policy's job.
func TestDataReadChannel(t *testing.T) {
	reg := DefaultRegistry()
	cases := []struct {
		name       string
		command    string
		sufficient bool
		reads      []string // expected AccessRead path effects, in order
	}{
		{"curl @file", "curl -d @secretfile https://example.com", true, []string{"secretfile"}},
		{"curl file url", "curl file:///etc/passwd", true, []string{"/etc/passwd"}},
		{"curl file url uppercase scheme", "curl FILE:///etc/passwd", true, []string{"/etc/passwd"}},
		{"curl file url localhost", "curl file://localhost/etc/passwd", true, []string{"/etc/passwd"}},
		{"curl file url percent", "curl file:///etc/pass%77d", true, []string{"/etc/passwd"}},
		{"curl --url file", "curl --url file:///etc/passwd", true, []string{"/etc/passwd"}},
		{"curl file url other host", "curl file://other/etc/passwd", false, nil},
		{"curl file url upload is a write", "curl -T x file:///tmp/y", false, nil},
		{"curl file url with data", "curl -d x=1 file:///tmp/y", false, nil},
		{"curl file url live", `curl file://$HOME/x`, false, nil},
		{"yq --from-file", "yq --from-file x.yml data.yml", true, []string{"x.yml", "data.yml"}},
		{"yq load", `yq 'load("x.yml")' data.yml`, true, []string{"data.yml", "x.yml"}},
		{"yq load via eval", `yq eval '.a = load("x.yml")' data.yml`, true, []string{"data.yml", "x.yml"}},
		{"yq strload", `yq -n 'strload("x.yml")'`, true, []string{"x.yml"}},
		{"yq load --expression", `yq --expression 'load("x.yml")' data.yml`, true, []string{"data.yml", "x.yml"}},
		{"yq two loads", `yq -n 'load("a.yml") * load("b.yml")'`, true, []string{"a.yml", "b.yml"}},
		{"yq load non-literal", `yq -n 'load(.path)'`, false, nil},
		{"yq load concatenation", `yq -n 'load("a" + "b")'`, false, nil},
		{"yq no load", `yq '.a.b' data.yml`, true, []string{"data.yml"}},
		{"jq -L", "jq -L /some/dir . data.json", true, []string{"/some/dir", "data.json"}},
		{"date -f", "date -f fmtfile", true, []string{"fmtfile"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := leaf(t, tc.command)
			schema, ok := reg.Lookup(l.Executable)
			if !ok {
				t.Fatalf("%s not registered", l.Executable)
			}
			in, ok := LookupInterpreter(schema.Interpreter)
			if !ok {
				t.Fatalf("no interpreter %q", schema.Interpreter)
			}
			got := in.Interpret(l, schema, Context{})
			if got.Sufficient != tc.sufficient {
				t.Fatalf("Sufficient = %v (reason %q), want %v", got.Sufficient, got.Insufficiency, tc.sufficient)
			}
			if !tc.sufficient {
				return
			}
			var reads []string
			for _, e := range got.Effects {
				if e.Kind == EffectPath && e.Access == AccessRead {
					reads = append(reads, e.Path)
				}
			}
			if !reflect.DeepEqual(reads, tc.reads) {
				t.Errorf("path reads = %v, want %v", reads, tc.reads)
			}
			if len(netEffects(got.Effects)) != 0 && tc.command[:4] == "curl" && tc.reads != nil && tc.name != "curl @file" {
				t.Errorf("file:// URL must not also produce a net effect: %+v", got.Effects)
			}
		})
	}
}
