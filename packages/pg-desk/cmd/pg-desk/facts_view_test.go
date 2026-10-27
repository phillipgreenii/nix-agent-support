package main

import (
	"encoding/json"
	"testing"
)

// deskCIStatus splits "no runs counted" into none (the stored checks_rollup
// says no checks at all, or is unavailable) and unknown (it says checks exist),
// per the 2026-10-27 operator ruling (bead pg2-kwxcb).
func TestDeskCIStatus(t *testing.T) {
	runs := func(s string) json.RawMessage { return json.RawMessage(`{"runs":[` + s + `]}`) }
	pass := `{"status":"completed","conclusion":"success"}`
	fail := `{"status":"completed","conclusion":"failure"}`
	cases := []struct {
		name   string
		raw    json.RawMessage
		checks string
		want   string
	}{
		{"no payload, no checks_rollup", nil, "", "none"},
		{"no payload, checks_rollup none", nil, "none", "none"},
		{"no payload, checks_rollup success", nil, "success", "unknown"},
		{"empty runs, checks_rollup none", runs(""), "none", "none"},
		{"empty runs, checks_rollup success", runs(""), "success", "unknown"},
		{"empty runs, checks_rollup failure", runs(""), "failure", "unknown"},
		{"malformed payload, checks_rollup pending", json.RawMessage(`{`), "pending", "unknown"},
		{"malformed payload, no checks_rollup", json.RawMessage(`{`), "", "none"},
		{"passing run wins over checks_rollup none", runs(pass), "none", "success"},
		{"failing run wins over checks_rollup success", runs(fail), "success", "failure"},
		{"in-flight run", runs(`{"status":"in_progress"}`), "success", "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deskCIStatus(tc.raw, tc.checks); got != tc.want {
				t.Errorf("deskCIStatus(%s, %q) = %q; want %q", tc.raw, tc.checks, got, tc.want)
			}
		})
	}
}
