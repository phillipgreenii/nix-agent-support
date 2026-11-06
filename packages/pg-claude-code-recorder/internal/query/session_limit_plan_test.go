package query

import "testing"

func TestPinsSession(t *testing.T) {
	for f, want := range map[string]bool{
		`{"field":"session_id","eq":"s"}`:                                        true,
		`{"field":"session","in":["s"]}`:                                         true,
		`{"all":[{"field":"type","eq":"user"},{"field":"session_id","eq":"s"}]}`: true,
		`{"field":"type","eq":"user"}`:                                           false,
		`{"any":[{"field":"session_id","eq":"s"},{"field":"type","eq":"u"}]}`:    false,
		`{"not":{"field":"session_id","eq":"s"}}`:                                false,
	} {
		var flt Filter
		if e := Decode([]byte(f), &flt); e != nil {
			t.Fatal(e)
		}
		if got := pinsSession(&flt); got != want {
			t.Errorf("%s: %v want %v", f, got, want)
		}
	}
	if pinsSession(nil) {
		t.Error("nil filter pins a session")
	}
}
