package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
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
		{"with before rfc3339", []string{"flaky", "--before", "2026-09-18T00:00:00Z"}, "flaky", "", false},
		{"no query", []string{}, "", "", true},
		{"two positional", []string{"flaky", "other"}, "", "", true},
		{"dangling --session", []string{"flaky", "--session"}, "", "", true},
		{"invalid --since", []string{"flaky", "--since", "not-a-time"}, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			query, sessionID, _, _, err := parseSearchArgs(c.args, time.Now())
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
}

func TestSearchSessions_FindsMatchInOneTranscript(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-repo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projDir, "s1.jsonl")
	line := `{"type":"assistant","timestamp":"2026-09-18T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"the payments test is flaky"}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions := []*session.Session{{SessionID: "s1", Cwd: "/repo"}}
	var buf bytes.Buffer
	if err := searchSessions(&buf, home, sessions, "flaky", "", time.Time{}, time.Time{}); err != nil {
		t.Fatalf("searchSessions: %v", err)
	}

	var doc searchJSONDoc
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Query != "flaky" || len(doc.Matches) != 1 {
		t.Fatalf("got %+v", doc)
	}
	if doc.Matches[0].SessionID != "s1" || doc.Matches[0].Role != "assistant" || doc.Matches[0].Timestamp == "" {
		t.Errorf("match wrong: %+v", doc.Matches[0])
	}
}

func TestSearchSessions_ScopedToOneSession(t *testing.T) {
	home := t.TempDir()
	sessions := []*session.Session{{SessionID: "s1", Cwd: "/repo"}, {SessionID: "s2", Cwd: "/repo"}}
	var buf bytes.Buffer
	// Neither session has a real transcript on disk; --session s2 must not
	// error just because s1 (unresolvable) is in the broader session list.
	if err := searchSessions(&buf, home, sessions, "x", "s2", time.Time{}, time.Time{}); err != nil {
		t.Fatalf("searchSessions: %v", err)
	}
	var doc searchJSONDoc
	_ = json.Unmarshal(buf.Bytes(), &doc)
	if len(doc.Matches) != 0 {
		t.Errorf("expected no matches for an unresolvable transcript, got %+v", doc.Matches)
	}
}

func TestSearchSessions_SinceExcludesEarlierMatch(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-repo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projDir, "s1.jsonl")
	line := `{"type":"assistant","timestamp":"2026-09-18T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"the payments test is flaky"}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions := []*session.Session{{SessionID: "s1", Cwd: "/repo"}}
	since := time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	if err := searchSessions(&buf, home, sessions, "flaky", "", since, time.Time{}); err != nil {
		t.Fatalf("searchSessions: %v", err)
	}
	var doc searchJSONDoc
	_ = json.Unmarshal(buf.Bytes(), &doc)
	if len(doc.Matches) != 0 {
		t.Errorf("expected the match to be excluded by --since, got %+v", doc.Matches)
	}
}
