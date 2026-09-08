package cmddesc

import (
	"reflect"
	"testing"
)

// TestSshIsRegistryOnlyDispatch mirrors TestKubectlIsRegistryOnlyDispatch:
// sshInterpreter reads only the schema and the leaf, never schema.Name.
func TestSshIsRegistryOnlyDispatch(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("ssh")
	if !ok {
		t.Fatal("ssh not registered")
	}
	if schema.Interpreter != "ssh" {
		t.Fatalf("ssh names interpreter %q, want \"ssh\"", schema.Interpreter)
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("ssh interpreter not registered")
	}
	l := leaf(t, "ssh host uptime")
	got := in.Interpret(l, schema, Context{})
	renamed := schema
	renamed.Name = "not-ssh"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name:\n%+v\n%+v", got, again)
	}
}

func netEffect(effects []Effect) (Effect, bool) {
	for _, e := range effects {
		if e.Kind == EffectNet {
			return e, true
		}
	}
	return Effect{}, false
}

// TestSshHostExtraction covers the three host spellings the brief names:
// plain "[user@]host", and an "ssh://" URL — urlHost (shared with curl) does
// the actual parsing; this just proves sshInterpreter routes the host
// operand through it.
func TestSshHostExtraction(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("ssh")
	in, _ := LookupInterpreter(schema.Interpreter)

	cases := []struct {
		command  string
		wantHost string
	}{
		{"ssh host uptime", "host"},
		{"ssh user@host uptime", "host"},
		{"ssh ssh://user@host:2222/ uptime", "host"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			res := in.Interpret(leaf(t, tc.command), schema, Context{})
			e, ok := netEffect(res.Effects)
			if !ok {
				t.Fatalf("no EffectNet in %+v", res.Effects)
			}
			if e.Host != tc.wantHost {
				t.Errorf("host = %q, want %q", e.Host, tc.wantHost)
			}
			if e.Dynamic {
				t.Errorf("host unexpectedly Dynamic")
			}
			if e.Direction != NetOutbound {
				t.Errorf("direction = %v, want NetOutbound", e.Direction)
			}
		})
	}
}

// TestSshDynamicHost: a runtime-expanded host operand is Dynamic and keeps
// the raw token as Host (curlInterpreter's own convention for a dynamic
// URL), never resolved.
func TestSshDynamicHost(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("ssh")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, `ssh "$H" uptime`), schema, Context{})
	e, ok := netEffect(res.Effects)
	if !ok {
		t.Fatalf("no EffectNet in %+v", res.Effects)
	}
	if !e.Dynamic {
		t.Errorf("expected Dynamic host")
	}
	if e.Host != "$H" {
		t.Errorf("host = %q, want raw token %q", e.Host, "$H")
	}
}

// TestSshRemoteCommandChild: the remote-command positionals become ONE
// "shell"-dialect ChildInvocation, joined by spaces, tagged Remote with the
// host — never split into per-word children, and never routed through any
// other dialect.
func TestSshRemoteCommandChild(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("ssh")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "ssh host cat /etc/passwd"), schema, Context{})
	if len(res.Children) != 1 {
		t.Fatalf("children = %+v, want exactly 1", res.Children)
	}
	c := res.Children[0]
	if c.Dialect != "shell" {
		t.Errorf("dialect = %q, want \"shell\"", c.Dialect)
	}
	if c.Program != "cat /etc/passwd" {
		t.Errorf("program = %q, want %q", c.Program, "cat /etc/passwd")
	}
	if c.Remote != "host" {
		t.Errorf("remote = %q, want %q", c.Remote, "host")
	}
}

// TestSshNoRemoteCommandIsInsufficient: a host with no remote command is an
// interactive session — insufficient, but the connection's own EffectNet is
// still emitted (ssh always connects) and no child is produced.
func TestSshNoRemoteCommandIsInsufficient(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("ssh")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "ssh host"), schema, Context{})
	if res.Sufficient {
		t.Errorf("expected insufficient (interactive session)")
	}
	if _, ok := netEffect(res.Effects); !ok {
		t.Errorf("expected an EffectNet even with no remote command")
	}
	if len(res.Children) != 0 {
		t.Errorf("expected no children, got %+v", res.Children)
	}
}

// TestSshNoHostIsInsufficient: no positionals at all — no host, no
// connection can even be described.
func TestSshNoHostIsInsufficient(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("ssh")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "ssh -4"), schema, Context{})
	if res.Sufficient {
		t.Errorf("expected insufficient (no host)")
	}
	if _, ok := netEffect(res.Effects); ok {
		t.Errorf("expected no EffectNet with no host at all")
	}
}

// TestSshKeyMaterialIsNotAPathRead: -i FILE is a KEY REFERENCE
// (EffectKeyMaterial), never an EffectPath — see cmddesc.KindKeyMaterial's
// doc comment for why: routing it through the ordinary read effect would
// make NoReadOfSecretPath Forbid every `-i ~/.ssh/id_rsa` invocation.
func TestSshKeyMaterialIsNotAPathRead(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("ssh")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "ssh -i ~/.ssh/id_rsa host uptime"), schema, Context{})
	var sawKeyMaterial bool
	for _, e := range res.Effects {
		if e.Kind == EffectPath {
			t.Errorf("unexpected EffectPath for -i's value: %+v", e)
		}
		if e.Kind == EffectKeyMaterial {
			sawKeyMaterial = true
			if e.Path != "~/.ssh/id_rsa" {
				t.Errorf("key material path = %q, want %q", e.Path, "~/.ssh/id_rsa")
			}
		}
	}
	if !sawKeyMaterial {
		t.Errorf("expected an EffectKeyMaterial for -i, got %+v", res.Effects)
	}
}

// TestSshFPathReadAndETruncate: -F is an ordinary PathRead (ssh reads and
// applies the config file's content); -E is a PathTruncate (ssh's own
// debug-log flag).
func TestSshFPathReadAndETruncate(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("ssh")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "ssh -F myconfig -E mylog host uptime"), schema, Context{})
	var sawRead, sawTruncate bool
	for _, e := range res.Effects {
		if e.Kind != EffectPath {
			continue
		}
		switch {
		case e.Path == "myconfig" && e.Access == AccessRead:
			sawRead = true
		case e.Path == "mylog" && e.Access == AccessTruncate:
			sawTruncate = true
		}
	}
	if !sawRead {
		t.Errorf("expected a PathRead for -F's value, got %+v", res.Effects)
	}
	if !sawTruncate {
		t.Errorf("expected a PathTruncate for -E's value, got %+v", res.Effects)
	}
}

// TestSshPositionalsEndOptions: a remote-command word that looks like a
// flag must never be mistaken for an ssh flag of its own (getopt's
// `+`/POSIXLY_CORRECT convention, PositionalsEndOptions).
func TestSshPositionalsEndOptions(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("ssh")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "ssh host -rf /"), schema, Context{})
	if !res.Sufficient {
		t.Fatalf("expected sufficient (host + remote command), got insufficient: %s", res.Insufficiency)
	}
	if len(res.Children) != 1 || res.Children[0].Program != "-rf /" {
		t.Errorf("children = %+v, want one child with program %q", res.Children, "-rf /")
	}
}

// TestSshUnknownFlagIsInsufficient: an unmodeled ssh flag before the host
// fails closed like any other schema (UnknownFlagInsufficient, the
// default).
func TestSshUnknownFlagIsInsufficient(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("ssh")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "ssh --not-a-real-flag host uptime"), schema, Context{})
	if res.Sufficient {
		t.Errorf("expected insufficient for an unmodeled flag")
	}
}
