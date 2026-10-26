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
