package cmddesc

import (
	"reflect"
	"testing"
)

// effectsOfKind returns the effects of one kind.
func effectsOfKind(effs []Effect, k EffectKind) []Effect {
	var out []Effect
	for _, e := range effs {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// TestConfigSourceEffect pins every config-source flag the packet names
// (tc-o14i5.3.5, design item e): each yields an EffectConfigSource carrying
// the path as written (P14: fact only, no verdict).
func TestConfigSourceEffect(t *testing.T) {
	rows := []struct {
		command string
		path    string
		detail  string
	}{
		{"treefmt --config-file /opt/t.toml", "/opt/t.toml", ""},
		{"ssh -F /opt/ssh_config host uptime", "/opt/ssh_config", ""},
		{"ssh -o Include=/opt/inc host uptime", "/opt/inc", ""},
		{"curl -K /opt/curlrc https://example.invalid/", "/opt/curlrc", ""},
		{"curl --config=/opt/curlrc https://example.invalid/", "/opt/curlrc", ""},
		{"sqlite3 -init /opt/init.sql", "/opt/init.sql", ""},
		{"scp -F /opt/ssh_config a b", "/opt/ssh_config", ""},
		{"just -f /opt/justfile", "/opt/justfile", ""},
		{"just --justfile /opt/justfile", "/opt/justfile", ""},
		{"just -d /opt/dir", "/opt/dir", ""},
		{"nix develop github:evil/x#shell", "github:evil/x#shell", FlakeRefDetail},
		{"nix shell nixpkgs#hello", "nixpkgs#hello", FlakeRefDetail},
		{"nix develop", ".", FlakeRefDetail}, // the implicit current-directory flake
	}
	for _, r := range rows {
		got := interpretDefault(t, r.command)
		cs := effectsOfKind(got.Effects, EffectConfigSource)
		if len(cs) != 1 || cs[0].Path != r.path || cs[0].Detail != r.detail || cs[0].Dynamic {
			t.Errorf("%q: config-source effects = %+v, want one with Path %q Detail %q", r.command, cs, r.path, r.detail)
		}
	}
	// stdin and runtime values are Dynamic.
	if cs := effectsOfKind(interpretDefault(t, "curl -K - https://example.invalid/").Effects, EffectConfigSource); len(cs) != 1 || !cs[0].Dynamic {
		t.Errorf("curl -K -: %+v, want one Dynamic config-source (stdin)", cs)
	}
	if cs := effectsOfKind(interpretDefault(t, `treefmt --config-file "$CFG"`).Effects, EffectConfigSource); len(cs) != 1 || !cs[0].Dynamic {
		t.Errorf("treefmt --config-file $CFG: %+v, want one Dynamic config-source", cs)
	}
}

// TestExecProgramEffect pins every exec-program flag the packet names. A
// command line becomes a shell child as well (so its own effects are judged);
// a path or opaque text does not.
func TestExecProgramEffect(t *testing.T) {
	rows := []struct {
		command string
		program string
		dialect string
		child   bool
	}{
		{"ssh -o ProxyCommand='nc %h %p' host uptime", "nc %h %p", ExecDialectShell, true},
		{"ssh -o 'ProxyCommand nc %h %p' host uptime", "nc %h %p", ExecDialectShell, true},
		{"ssh -oLocalCommand=/opt/lc host uptime", "/opt/lc", ExecDialectShell, true},
		{"ssh -o KnownHostsCommand=/opt/khc host uptime", "/opt/khc", ExecDialectShell, true},
		{"ssh -o PKCS11Provider=/opt/lib.so host uptime", "/opt/lib.so", ExecDialectPath, false},
		{"go test -exec /opt/xprog ./...", "/opt/xprog", ExecDialectShell, true},
		{"go build -toolexec /opt/tx ./...", "/opt/tx", ExecDialectShell, true},
		{"go run -exec /opt/xprog ./cmd/x", "/opt/xprog", ExecDialectShell, true},
		{"nix build --post-build-hook /opt/hook .#x", "/opt/hook", "", false},
		{"nix build --option post-build-hook /opt/hook .#x", "/opt/hook", "", false},
		{"nix --option diff-hook /opt/dh build .#x", "/opt/dh", "", false},
		{"nix run --build-hook /opt/bh .", "/opt/bh", "", false},
		{"sqlite3 -cmd '.shell id' /opt/db.sqlite", ".shell id", "", false},
		{"git --exec-path=/opt/gx status", "/opt/gx", ExecDialectPath, false},
	}
	for _, r := range rows {
		got := interpretDefault(t, r.command)
		ep := effectsOfKind(got.Effects, EffectExecProgram)
		if len(ep) != 1 || ep[0].Program != r.program || ep[0].Dialect != r.dialect || ep[0].Dynamic {
			t.Errorf("%q: exec-program effects = %+v, want one with Program %q Dialect %q", r.command, ep, r.program, r.dialect)
		}
		var shell []ChildInvocation
		for _, c := range got.Children {
			if c.Dialect == "shell" && c.Program == r.program {
				shell = append(shell, c)
			}
		}
		if (len(shell) == 1) != r.child {
			t.Errorf("%q: shell children = %+v, want child=%v", r.command, got.Children, r.child)
		}
	}
	// A bare `git --exec-path` only prints the path.
	if ep := effectsOfKind(interpretDefault(t, "git --exec-path status").Effects, EffectExecProgram); len(ep) != 0 {
		t.Errorf("git --exec-path (no value): %+v, want none", ep)
	}
	// Runtime values are Dynamic and never become children.
	got := interpretDefault(t, `go test -exec "$X" ./...`)
	if ep := effectsOfKind(got.Effects, EffectExecProgram); len(ep) != 1 || !ep[0].Dynamic || len(got.Children) != 0 {
		t.Errorf("go test -exec $X: %+v children %+v, want one Dynamic exec-program and no child", ep, got.Children)
	}
	// nix --option with a setting that is not an exec source stays unmodeled.
	if got := interpretDefault(t, "nix build --option sandbox false .#x"); got.Sufficient {
		t.Error("nix build --option sandbox false: sufficient, want the unmodeled setting to fail closed")
	}
}

// TestGitConfigPairViaP16Table: git -c / --config-env defer to K9's P16 table
// per key (value facts override the key's class; --config-env's value is
// unknowable, so only the key's default class applies).
func TestGitConfigPairViaP16Table(t *testing.T) {
	rows := map[string]GitConfigClass{
		"git -c core.pager=less status":              GitConfigExecSink,
		"git -c core.fsmonitor=/tmp/x status":        GitConfigExecSink,
		"git -c core.fsmonitor=false status":         GitConfigInertMetadata,
		"git -c http.sslVerify=false status":         GitConfigInterlockDisable,
		"git -c user.name=x status":                  GitConfigUnlisted,
		"git --config-env=core.fsmonitor=FSM status": GitConfigExecSink, // value unknown: key default
		"git --config-env core.pager=PAGER status":   GitConfigExecSink,
	}
	for c, want := range rows {
		got := interpretDefault(t, c)
		if cls := configKeyClasses(got.Effects); !reflect.DeepEqual(cls, []GitConfigClass{want}) {
			t.Errorf("%q: classes = %v, want [%s]", c, cls, want)
		}
	}
	// An alias whose value starts with `!` also hands its shell text over.
	got := interpretDefault(t, "git -c 'alias.x=!echo hi' status")
	if len(got.Children) != 1 || got.Children[0].Program != "echo hi" {
		t.Errorf("-c alias.x=!echo hi: children = %+v, want the hidden shell", got.Children)
	}
}

func TestExecSourceEnvEffect(t *testing.T) {
	for _, tc := range []struct {
		name, value, dialect string
		ok                   bool
	}{
		{"GIT_EXEC_PATH", "/opt/gx", ExecDialectPath, true},
		{"SSH_ASKPASS", `"/opt/ap"`, ExecDialectPath, true},
		{"SSH_ASKPASS_REQUIRE", "force", "", true},
		{"GIT_DIR", "/x", "", false},
		{"PATH", "/x", "", false},
	} {
		e, ok := ExecSourceEnvEffect(tc.name, tc.value, false)
		if ok != tc.ok || (ok && (e.Kind != EffectExecProgram || e.Dialect != tc.dialect)) {
			t.Errorf("%s=%s: %+v ok=%v, want ok=%v dialect=%q", tc.name, tc.value, e, ok, tc.ok, tc.dialect)
		}
	}
	if e, _ := ExecSourceEnvEffect("SSH_ASKPASS", `"/opt/ap"`, false); e.Program != "/opt/ap" {
		t.Errorf("quotes not stripped: %q", e.Program)
	}
}
