package corpus

import (
	"testing"
	"time"
)

func TestCriteriaMatches(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-1 * time.Minute)
	old := now.Add(-10 * time.Minute)

	cases := []struct {
		name     string
		crit     Criteria
		class    FileClass
		mtime    time.Time
		isActive bool
		want     bool
	}{
		{
			name:  "class not in Classes -> false",
			crit:  Criteria{Classes: []FileClass{Transcript}},
			class: Subagent, mtime: recent, isActive: true, want: false,
		},
		{
			name:  "class in Classes, no other gates -> true",
			crit:  Criteria{Classes: []FileClass{Transcript, Subagent}},
			class: Subagent, mtime: old, isActive: false, want: true,
		},
		{
			name:  "Window>0 and mtime older than window -> false",
			crit:  Criteria{Classes: []FileClass{Transcript}, Window: 5 * time.Minute},
			class: Transcript, mtime: old, isActive: true, want: false,
		},
		{
			name:  "Window>0 and mtime within window -> true",
			crit:  Criteria{Classes: []FileClass{Transcript}, Window: 5 * time.Minute},
			class: Transcript, mtime: recent, isActive: true, want: true,
		},
		{
			name:  "Window==0 disables age gate (old file still matches)",
			crit:  Criteria{Classes: []FileClass{Transcript}, Window: 0},
			class: Transcript, mtime: old, isActive: true, want: true,
		},
		{
			name:  "ActiveOnly and not active -> false",
			crit:  Criteria{Classes: []FileClass{Transcript}, ActiveOnly: true},
			class: Transcript, mtime: recent, isActive: false, want: false,
		},
		{
			name:  "ActiveOnly and active -> true",
			crit:  Criteria{Classes: []FileClass{Transcript}, ActiveOnly: true},
			class: Transcript, mtime: recent, isActive: true, want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.crit.matches(tc.class, tc.mtime, tc.isActive, now)
			if got != tc.want {
				t.Fatalf("matches(class=%v, mtime=%v, active=%v) = %v, want %v",
					tc.class, tc.mtime, tc.isActive, got, tc.want)
			}
		})
	}
}
