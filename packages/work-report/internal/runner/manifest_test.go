//go:build unix

package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireRule asserts the failure reason names the rule and, when path is not
// empty, the offending path.
func requireRule(t *testing.T, out Outcome, rule, path string) {
	t.Helper()
	r := out.Record.Reason
	if !strings.Contains(r, `"`+rule+`"`) {
		t.Fatalf("reason %q does not name rule %q", r, rule)
	}
	if path != "" && !strings.Contains(r, `"`+path+`"`) {
		t.Fatalf("reason %q does not name path %q", r, path)
	}
}

func manifestWith(files string, extra ...string) string {
	s := `{"contract_version":1,"files":` + files
	for _, e := range extra {
		s += "," + e
	}
	return s + "}"
}

func entry(path string) string {
	b, _ := jsonString(path)
	return `{"path":` + b + `,"content_type":"text/plain"}`
}

func TestManifestRules(t *testing.T) {
	real1 := scriptFile{Path: "real.md", Content: "content\n"}
	cases := []struct {
		name   string
		s      script
		kind   string
		rule   string
		path   string // expected to appear in the reason
		outsid bool   // creates an outside file the script links to
	}{
		{name: "zero files", s: script{Stdout: manifestWith(`[]`)}, kind: KindInvalidManifest, rule: ruleFiles},
		{name: "files null", s: script{Stdout: manifestWith(`null`)}, kind: KindInvalidManifest, rule: ruleFiles},
		{name: "files missing", s: script{Stdout: `{"contract_version":1}`}, kind: KindInvalidManifest, rule: ruleFiles},
		{name: "files not an array", s: script{Stdout: manifestWith(`{}`)}, kind: KindInvalidManifest, rule: ruleFiles},
		{name: "contract_version missing", s: script{Stdout: `{"files":[` + entry("real.md") + `]}`, Files: []scriptFile{real1}}, kind: KindInvalidManifest, rule: ruleContractVersion},
		{name: "contract_version a string", s: script{Stdout: `{"contract_version":"1","files":[` + entry("real.md") + `]}`, Files: []scriptFile{real1}}, kind: KindInvalidManifest, rule: ruleContractVersion},
		{name: "wrong contract_version", s: script{Stdout: `{"contract_version":2,"files":[` + entry("real.md") + `]}`, Files: []scriptFile{real1}}, kind: KindContractVersion},
		{name: "invalid JSON", s: script{Stdout: `not json`}, kind: KindInvalidManifest, rule: ruleJSON},
		{name: "empty stdout", s: script{}, kind: KindInvalidManifest, rule: ruleJSON},
		{name: "JSON null", s: script{Stdout: `null`}, kind: KindInvalidManifest, rule: ruleJSON},
		{name: "two JSON values", s: script{Stdout: `{"contract_version":1,"files":[]} {}`}, kind: KindInvalidManifest, rule: ruleJSON},
		{name: "absolute path", s: script{Stdout: manifestWith(`[` + entry("/etc/passwd") + `]`)}, kind: KindInvalidManifest, rule: rulePathRelative, path: "/etc/passwd"},
		{name: "dotdot", s: script{Stdout: manifestWith(`[` + entry("../x") + `]`)}, kind: KindInvalidManifest, rule: rulePathDotDot, path: "../x"},
		{name: "dotdot inside", s: script{Stdout: manifestWith(`[` + entry("a/../b") + `]`)}, kind: KindInvalidManifest, rule: rulePathDotDot, path: "a/../b"},
		{name: "NUL", s: script{Stdout: manifestWith(`[` + entry("a\x00b") + `]`)}, kind: KindInvalidManifest, rule: rulePathNUL},
		{name: "backslash", s: script{Stdout: manifestWith(`[` + entry(`a\b`) + `]`)}, kind: KindInvalidManifest, rule: rulePathBackslash},
		{name: "trailing slash", s: script{Stdout: manifestWith(`[` + entry("dir/") + `]`)}, kind: KindInvalidManifest, rule: rulePathTrailing, path: "dir/"},
		{name: "not clean", s: script{Stdout: manifestWith(`[` + entry("a//b") + `]`)}, kind: KindInvalidManifest, rule: rulePathClean, path: "a//b"},
		{name: "dot slash manifest.json", s: script{Stdout: manifestWith(`[` + entry("./manifest.json") + `]`)}, kind: KindInvalidManifest, rule: rulePathClean, path: "./manifest.json"},
		{name: "manifest.json", s: script{Stdout: manifestWith(`[` + entry("manifest.json") + `]`)}, kind: KindInvalidManifest, rule: rulePathReserved, path: "manifest.json"},
		{name: "Manifest.JSON", s: script{Stdout: manifestWith(`[` + entry("Manifest.JSON") + `]`)}, kind: KindInvalidManifest, rule: rulePathReserved, path: "Manifest.JSON"},
		{name: "MANIFEST.json", s: script{Stdout: manifestWith(`[` + entry("MANIFEST.json") + `]`)}, kind: KindInvalidManifest, rule: rulePathReserved, path: "MANIFEST.json"},
		{name: "stderr.log", s: script{Stdout: manifestWith(`[` + entry("stderr.log") + `]`)}, kind: KindInvalidManifest, rule: rulePathReserved, path: "stderr.log"},
		{name: "duplicate differing by case", s: script{Files: []scriptFile{{Path: "A.txt", Content: "x"}}, Stdout: manifestWith(`[` + entry("A.txt") + `,` + entry("a.txt") + `]`)}, kind: KindInvalidManifest, rule: rulePathUnique, path: "a.txt"},
		{name: "duplicate NFC versus NFD", s: script{Files: []scriptFile{{Path: "café.txt", Content: "x"}}, Stdout: manifestWith(`[` + entry("café.txt") + `,` + entry("café.txt") + `]`)}, kind: KindInvalidManifest, rule: rulePathUnique},
		{name: "exact duplicate", s: script{Files: []scriptFile{real1}, Stdout: manifestWith(`[` + entry("real.md") + `,` + entry("real.md") + `]`)}, kind: KindInvalidManifest, rule: rulePathUnique, path: "real.md"},
		{name: "symlink escaping the directory", s: script{Stdout: manifestWith(`[` + entry("esc.txt") + `]`)}, kind: KindInvalidManifest, rule: ruleInside, path: "esc.txt", outsid: true},
		{name: "symlink to manifest.json", s: script{
			Files:    []scriptFile{{Path: "manifest.json", Content: "planted"}},
			Symlinks: []scriptLink{{Path: "alias.txt", Target: "manifest.json"}},
			Stdout:   manifestWith(`[` + entry("alias.txt") + `]`),
		}, kind: KindInvalidManifest, rule: ruleResolvedRes, path: "alias.txt"},
		{name: "symlink to stderr.log", s: script{
			Files:    []scriptFile{{Path: "stderr.log", Content: "planted"}},
			Symlinks: []scriptLink{{Path: "alias.txt", Target: "stderr.log"}},
			Stdout:   manifestWith(`[` + entry("alias.txt") + `]`),
		}, kind: KindInvalidManifest, rule: ruleResolvedRes, path: "alias.txt"},
		{name: "two listed names, one resolved file", s: script{
			Files:    []scriptFile{real1},
			Symlinks: []scriptLink{{Path: "link.md", Target: "real.md"}},
			Stdout:   manifestWith(`[` + entry("real.md") + `,` + entry("link.md") + `]`),
		}, kind: KindInvalidManifest, rule: ruleResolvedUniq, path: "link.md"},
		{name: "hard link", s: script{
			Files:     []scriptFile{real1},
			Hardlinks: []scriptLink{{Path: "hard.md", Target: "real.md"}},
			Stdout:    manifestWith(`[` + entry("hard.md") + `]`),
		}, kind: KindInvalidManifest, rule: ruleLinkCount, path: "hard.md"},
		{name: "directory entry", s: script{Dirs: []string{"d"}, Stdout: manifestWith(`[` + entry("d") + `]`)}, kind: KindInvalidManifest, rule: ruleRegular, path: "d"},
		{name: "empty file", s: script{Files: []scriptFile{{Path: "empty.md"}}, Stdout: manifestWith(`[` + entry("empty.md") + `]`)}, kind: KindInvalidManifest, rule: ruleNonEmpty, path: "empty.md"},
		{name: "missing file", s: script{Stdout: manifestWith(`[` + entry("gone.md") + `]`)}, kind: KindInvalidManifest, rule: ruleResolved, path: "gone.md"},
		{name: "content_type missing", s: script{Files: []scriptFile{real1}, Stdout: manifestWith(`[{"path":"real.md"}]`)}, kind: KindInvalidManifest, rule: ruleContentType, path: "real.md"},
		{name: "content_type malformed", s: script{Files: []scriptFile{real1}, Stdout: manifestWith(`[{"path":"real.md","content_type":"notamediatype"}]`)}, kind: KindInvalidManifest, rule: ruleContentType, path: "real.md"},
		{name: "content_type not a string", s: script{Files: []scriptFile{real1}, Stdout: manifestWith(`[{"path":"real.md","content_type":7}]`)}, kind: KindInvalidManifest, rule: ruleContentType, path: "real.md"},
		{name: "file item not an object", s: script{Stdout: manifestWith(`["real.md"]`)}, kind: KindInvalidManifest, rule: ruleFileItem},
		{name: "path not a string", s: script{Stdout: manifestWith(`[{"path":3,"content_type":"text/plain"}]`)}, kind: KindInvalidManifest, rule: ruleFileItem},
		{name: "summary not a string", s: script{Files: []scriptFile{real1}, Stdout: manifestWith(`[`+entry("real.md")+`]`, `"summary":4`)}, kind: KindInvalidManifest, rule: ruleSummary},
		{name: "warnings not strings", s: script{Files: []scriptFile{real1}, Stdout: manifestWith(`[`+entry("real.md")+`]`, `"warnings":[1]`)}, kind: KindInvalidManifest, rule: ruleWarnings},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t)
			s := tc.s
			if tc.outsid {
				outside := filepath.Join(t.TempDir(), "outside.txt")
				if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
					t.Fatal(err)
				}
				s.Symlinks = append(s.Symlinks, scriptLink{Path: "esc.txt", Target: outside})
			}
			out := fx.run(fx.gen(s))
			requireKind(t, out, tc.kind)
			if tc.rule != "" {
				requireRule(t, out, tc.rule, tc.path)
			}
			if tc.kind == KindContractVersion && !strings.Contains(out.Record.Reason, "supports contract_version 1") {
				t.Fatalf("reason %q does not name the supported versions", out.Record.Reason)
			}
			// A failed run keeps its directory with the failure recorded.
			if m := readManifest(t, out.RunDir); m["status"] != "failed" || m["outcome_kind"] != tc.kind {
				t.Fatalf("manifest.json = %v", m)
			}
		})
	}
}

func TestManifestAcceptsGoodShapes(t *testing.T) {
	t.Run("content_type with parameters and unknown fields", func(t *testing.T) {
		fx := newFixture(t)
		out := fx.run(fx.gen(script{
			Files:  []scriptFile{{Path: "r.md", Content: "x"}},
			Stdout: `{"contract_version":1,"future":{"a":1},"files":[{"path":"r.md","content_type":"text/markdown; charset=utf-8","extra":true}]}`,
		}))
		requireOK(t, out)
		if out.Record.Files[0].ContentType != "text/markdown; charset=utf-8" {
			t.Fatalf("content_type = %q", out.Record.Files[0].ContentType)
		}
	})
	t.Run("a symlink inside the directory is fine", func(t *testing.T) {
		fx := newFixture(t)
		out := fx.run(fx.gen(script{
			Files:    []scriptFile{{Path: "real.md", Content: "content\n"}},
			Symlinks: []scriptLink{{Path: "link.md", Target: "real.md"}},
			Stdout:   okManifest("link.md"),
		}))
		requireOK(t, out)
		if f := out.Record.Files[0]; f.Path != "link.md" || f.SizeBytes != 8 {
			t.Fatalf("file = %+v", f)
		}
	})
	t.Run("unlisted files are left alone", func(t *testing.T) {
		fx := newFixture(t)
		s := okScript()
		s.Files = append(s.Files, scriptFile{Path: "scratch/notes.txt", Content: "left"})
		out := fx.run(fx.gen(s))
		requireOK(t, out)
		if _, err := os.Stat(filepath.Join(out.RunDir, "scratch", "notes.txt")); err != nil {
			t.Fatal(err)
		}
		for _, f := range out.Record.Files {
			if strings.Contains(f.Path, "scratch") {
				t.Fatalf("unlisted file reported: %+v", f)
			}
		}
	})
	t.Run("a symlinked output parent", func(t *testing.T) {
		fx := newFixture(t)
		real := filepath.Join(t.TempDir(), "real-parent")
		if err := os.MkdirAll(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link-parent")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		fx.parent = link
		out := fx.run(fx.gen(okScript()))
		requireOK(t, out)
		if _, err := os.Stat(filepath.Join(real, filepath.Base(out.RunDir), "report.md")); err != nil {
			t.Fatal(err)
		}
	})
}

func jsonString(s string) (string, error) {
	b, err := jsonMarshalString(s)
	return string(b), err
}
