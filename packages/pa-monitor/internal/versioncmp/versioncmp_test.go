package versioncmp

import "testing"

func TestMismatch(t *testing.T) {
	cases := []struct {
		name   string
		client string
		daemon string
		want   bool
	}{
		{"equal", "26.07.08+aa", "26.07.08+aa", false},
		{"differ", "26.07.08+aa", "26.07.01+bb", true},
		{"client empty", "", "26.07.08+aa", false},
		{"daemon empty", "26.07.08+aa", "", false},
		{"both empty", "", "", false},
		{"dev vs dev", "dev", "dev", false},
		// PINNED user-approved decision: a dev client vs an installed daemon
		// WARNS. "dev" is treated as a normal value.
		{"dev vs real", "dev", "26.07.01+abcd1234", true},
		{"real vs dev", "26.07.08+aa", "dev", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Mismatch(tc.client, tc.daemon); got != tc.want {
				t.Errorf("Mismatch(%q, %q) = %v, want %v", tc.client, tc.daemon, got, tc.want)
			}
		})
	}
}
