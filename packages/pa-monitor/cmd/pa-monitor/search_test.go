package main

import (
	"strconv"
	"testing"
	"time"
)

func TestParseSearchArgs(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		wantQuery     string
		wantSessionID string
		wantErr       bool
	}{
		{"bare query", []string{"flaky"}, "flaky", "", false},
		{"json before query", []string{"--json", "flaky"}, "flaky", "", false},
		{"json after query", []string{"flaky", "--json"}, "flaky", "", false},
		{"with session", []string{"flaky", "--session", "s1"}, "flaky", "s1", false},
		{"session before query", []string{"--session", "s1", "flaky"}, "flaky", "s1", false},
		{"with since duration", []string{"flaky", "--since", "24h"}, "flaky", "", false},
		{"with since days", []string{"flaky", "--since", "7d"}, "flaky", "", false},
		{"with before rfc3339", []string{"flaky", "--before", "2026-09-18T00:00:00Z"}, "flaky", "", false},
		{"since older than 7 days", []string{"flaky", "--since", "8d"}, "", "", true},
		{"no query", []string{}, "", "", true},
		{"two positional", []string{"flaky", "other"}, "", "", true},
		{"dangling --session", []string{"flaky", "--session"}, "", "", true},
		{"invalid --since", []string{"flaky", "--since", "not-a-time"}, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			query, sessionID, _, _, _, err := parseSearchArgs(c.args, time.Now())
			if c.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSearchArgs: %v", err)
			}
			if query != c.wantQuery || sessionID != c.wantSessionID {
				t.Errorf("got query=%q sessionID=%q, want query=%q sessionID=%q", query, sessionID, c.wantQuery, c.wantSessionID)
			}
		})
	}
}

func TestParseTimeBound(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	got, err := parseTimeBound("24h", now)
	if err != nil {
		t.Fatalf("parseTimeBound(24h): %v", err)
	}
	if want := now.Add(-24 * time.Hour); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	got, err = parseTimeBound("2026-09-17T00:00:00Z", now)
	if err != nil {
		t.Fatalf("parseTimeBound(rfc3339): %v", err)
	}
	want := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if _, err := parseTimeBound("not-a-time", now); err == nil {
		t.Fatal("expected an error for an unparseable bound")
	}

	got, err = parseTimeBound("90m", now)
	if err != nil {
		t.Fatalf("parseTimeBound(90m): %v", err)
	}
	if want := now.Add(-90 * time.Minute); !got.Equal(want) {
		t.Errorf("90m: got %v, want %v", got, want)
	}
}

func TestParseTimeBound_Days(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"7d", now.Add(-7 * 24 * time.Hour)},
		{"0d", now},
		{"1d", now.Add(-24 * time.Hour)},
		{"007d", now.Add(-7 * 24 * time.Hour)},
		{"100000d", now.Add(-100000 * 24 * time.Hour)},
	}
	for _, c := range cases {
		got, err := parseTimeBound(c.in, now)
		if err != nil {
			t.Fatalf("parseTimeBound(%q): %v", c.in, err)
		}
		if !got.Equal(c.want) {
			t.Errorf("parseTimeBound(%q) = %v, want %v", c.in, got, c.want)
		}
	}

	for _, in := range []string{"7D", "d", "-7d", "1.5d", "100001d", "99999999999999999999d", "+7d", "1h30d", " 7d", "7d "} {
		if _, err := parseTimeBound(in, now); err == nil {
			t.Errorf("parseTimeBound(%q): expected an error", in)
		}
	}
}

// TestParseTimeBound_DaysAcrossDST proves "<N>d" is exactly N*24h of
// elapsed time even when a DST transition falls inside the window.
func TestParseTimeBound_DaysAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	cases := []struct {
		name string
		now  time.Time
		in   string
	}{
		// US spring-forward: 2026-03-08 (a 23h day); window spans it.
		{"spring forward 1d", time.Date(2026, 3, 9, 12, 0, 0, 0, loc), "1d"},
		{"spring forward 3d", time.Date(2026, 3, 10, 12, 0, 0, 0, loc), "3d"},
		// US fall-back: 2026-11-01 (a 25h day); window spans it.
		{"fall back 1d", time.Date(2026, 11, 2, 12, 0, 0, 0, loc), "1d"},
		{"fall back 7d", time.Date(2026, 11, 5, 12, 0, 0, 0, loc), "7d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseTimeBound(c.in, c.now)
			if err != nil {
				t.Fatalf("parseTimeBound(%q): %v", c.in, err)
			}
			n, _ := strconv.Atoi(c.in[:len(c.in)-1])
			if elapsed, want := c.now.Sub(got), time.Duration(n)*24*time.Hour; elapsed != want {
				t.Errorf("elapsed = %v, want exactly %v", elapsed, want)
			}
		})
	}
}

func TestParseSearchArgsSubstringFlag(t *testing.T) {
	_, _, _, _, sub, err := parseSearchArgs([]string{"flak", "--substring"}, time.Now())
	if err != nil || !sub {
		t.Fatalf("--substring: sub=%v err=%v", sub, err)
	}
	_, _, _, _, sub, err = parseSearchArgs([]string{"flaky"}, time.Now())
	if err != nil || sub {
		t.Fatalf("default must be word search: sub=%v err=%v", sub, err)
	}
}
