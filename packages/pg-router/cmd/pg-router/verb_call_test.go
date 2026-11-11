package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestShortTime(t *testing.T) {
	if got := shortTime(""); got != "-" {
		t.Errorf("shortTime(\"\") = %q", got)
	}
	if got := shortTime("not-a-time"); got != "not-a-time" {
		t.Errorf("shortTime(garbage) = %q, want the raw text", got)
	}
	if got := shortTime("2026-10-01T12:34:56Z"); len(got) != len("15:04:05") {
		t.Errorf("shortTime(valid) = %q, want HH:MM:SS", got)
	}
}

func TestRenderGates_emptyAndSorted(t *testing.T) {
	var buf bytes.Buffer
	renderGates(&buf, nil)
	if got := buf.String(); got != "  (none)\n" {
		t.Fatalf("empty render = %q", got)
	}
	buf.Reset()
	renderGates(&buf, []statusGate{{Type: "B", SetAt: "2026-10-01T12:00:00Z", Owner: "o"}, {Type: "A", SetAt: "2026-10-01T12:00:00Z"}})
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "  A:") || !strings.Contains(lines[1], "owner=o") {
		t.Fatalf("render = %q, want A before B with B's owner", buf.String())
	}
}

func TestParseGateFlags_connectionFlagsAndPositionals(t *testing.T) {
	fs, socket, token := gateFlags("test")
	pos, code, ok := parseGateFlags(fs, "test", []string{"one", "--socket", "/s", "two", "--token", "t"})
	if !ok || code != exitOK {
		t.Fatalf("parse = (%v, %d, %v)", pos, code, ok)
	}
	if *socket != "/s" || *token != "t" || len(pos) != 2 || pos[0] != "one" || pos[1] != "two" {
		t.Fatalf("socket=%q token=%q pos=%v", *socket, *token, pos)
	}
}
