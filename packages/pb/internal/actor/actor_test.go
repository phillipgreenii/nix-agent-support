package actor

import (
	"bytes"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDerive_fixedEntropyGivesAVersion4UUID(t *testing.T) {
	zero := bytes.NewReader(make([]byte, 16))
	r, err := Derive(Params{Role: "drain", Getenv: env(nil), Rand: zero})
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourceRandom || r.Actor != "00000000-0000-4000-8000-000000000000-drain" {
		t.Errorf("got %+v", r)
	}
}

func TestDerive_shortEntropyIsAnError(t *testing.T) {
	if _, err := Derive(Params{Role: "drain", Getenv: env(nil), Rand: strings.NewReader("short")}); err == nil {
		t.Error("want an error when the entropy source is exhausted")
	}
}

func TestUUIDInPath(t *testing.T) {
	const a, b = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	cases := map[string]string{
		"":                                   "",
		"   ":                                "",
		"/tmp/x/" + a + "/scratchpad":        a,
		"/tmp/" + a + "/y/" + b + "/s":       b,  // the LAST component wins
		"/tmp/x/" + a + "-suffix/scratchpad": "", // not a whole component
		"relative/" + a:                      a,
		"/tmp/x/" + a + "/":                  a,
	}
	for in, want := range cases {
		if got := uuidInPath(in); got != want {
			t.Errorf("uuidInPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidRole(t *testing.T) {
	for _, r := range Roles {
		if !ValidRole(r) {
			t.Errorf("%s should be valid", r)
		}
	}
	for _, r := range []string{"", "Drain", "drain ", "bogus"} {
		if ValidRole(r) {
			t.Errorf("%q should be invalid", r)
		}
	}
}

const (
	sidA = "11111111-1111-4111-8111-111111111111"
	sidB = "22222222-2222-4222-8222-222222222222"
	sidC = "33333333-3333-4333-8333-333333333333"
)

func ones() *bytes.Reader { return bytes.NewReader(bytes.Repeat([]byte{0xff}, 16)) }

func TestDerive_versionAndVariantBitsAreSetOnAnyEntropy(t *testing.T) {
	r, err := Derive(Params{Role: "drain", Getenv: env(nil), Rand: ones()})
	if err != nil {
		t.Fatal(err)
	}
	if r.SessionID != "ffffffff-ffff-4fff-bfff-ffffffffffff" {
		t.Errorf("session id = %q, want version nibble 4 and variant nibble b", r.SessionID)
	}
}

func TestDerive_everyBranchReportsItsSourceAndSuffix(t *testing.T) {
	pad := "/tmp/x/" + sidC + "/scratchpad"
	cases := []struct {
		name string
		p    Params
		want Result
	}{
		{
			"env",
			Params{Role: "drain", Getenv: env(map[string]string{"CLAUDE_SESSION_ID": sidA, "CLAUDE_CODE_SESSION_ID": sidB}), Scratchpad: pad},
			Result{Actor: sidA + "-drain", SessionID: sidA, Source: SourceEnv},
		},
		{
			"second env name",
			Params{Role: "unblock", Getenv: env(map[string]string{"CLAUDE_CODE_SESSION_ID": sidB}), Scratchpad: pad},
			Result{Actor: sidB + "-unblock", SessionID: sidB, Source: SourceEnv},
		},
		{
			"blank env is unset",
			Params{Role: "unstick", Getenv: env(map[string]string{"CLAUDE_SESSION_ID": "  ", "CLAUDE_CODE_SESSION_ID": ""}), Scratchpad: pad},
			Result{Actor: sidC + "-unstick", SessionID: sidC, Source: SourceScratchpad},
		},
		{
			"blank env is trimmed when set",
			Params{Role: "drain", Getenv: env(map[string]string{"CLAUDE_SESSION_ID": " " + sidA + " "})},
			Result{Actor: sidA + "-drain", SessionID: sidA, Source: SourceEnv},
		},
		{
			"random",
			Params{Role: "drain", Getenv: env(nil), Scratchpad: "/tmp/no-uuid-here", Rand: ones()},
			Result{Actor: "ffffffff-ffff-4fff-bfff-ffffffffffff-drain", SessionID: "ffffffff-ffff-4fff-bfff-ffffffffffff", Source: SourceRandom},
		},
	}
	for _, c := range cases {
		got, err := Derive(c.p)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDerive_rejectsAnUnknownRoleBeforeAnythingElse(t *testing.T) {
	_, err := Derive(Params{Role: "bogus", Getenv: env(map[string]string{"CLAUDE_SESSION_ID": sidA})})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{`"bogus"`, "drain, unblock, unstick"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestDerive_rejectsAnEnvIdOutsideTheSafeAlphabet(t *testing.T) {
	for _, bad := range []string{"a b", "a;b", "a$(x)", "a/b"} {
		_, err := Derive(Params{Role: "drain", Getenv: env(map[string]string{"CLAUDE_SESSION_ID": bad})})
		if err == nil || !strings.Contains(err.Error(), "CLAUDE_SESSION_ID") {
			t.Errorf("%q: err = %v, want one naming the variable", bad, err)
		}
	}
	for _, ok := range []string{"abc", "A.b_c-1", sidA} {
		if _, err := Derive(Params{Role: "drain", Getenv: env(map[string]string{"CLAUDE_SESSION_ID": ok})}); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestDerive_nilRandUsesTheSystemSource(t *testing.T) {
	a, err := Derive(Params{Role: "drain", Getenv: env(nil)})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Derive(Params{Role: "drain", Getenv: env(nil)})
	if a.Source != SourceRandom || a.Actor == b.Actor {
		t.Errorf("two system-random ids: %+v and %+v", a, b)
	}
}

func TestSourceValuesArePartOfTheContract(t *testing.T) {
	if SourceEnv != "env" || SourceScratchpad != "scratchpad" || SourceRandom != "random" {
		t.Errorf("sources = %q %q %q", SourceEnv, SourceScratchpad, SourceRandom)
	}
}

func TestUUIDInPath_firstComponentCounts(t *testing.T) {
	if got := uuidInPath(sidA + "/scratchpad"); got != sidA {
		t.Errorf("got %q, want the leading component", got)
	}
}

func TestRandomUUID_entropyFailureNamesTheCause(t *testing.T) {
	_, err := randomUUID(strings.NewReader("short"))
	if err == nil || !strings.Contains(err.Error(), "generate a random session id") {
		t.Errorf("err = %v", err)
	}
}
