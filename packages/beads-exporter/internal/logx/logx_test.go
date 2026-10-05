package logx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, line)
	}
	return m
}

func TestFieldContract(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelDebug)
	log.Debug("d")
	log.Info("i", "db", "alpha")
	log.Warn("w")
	log.Error("e")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("%d lines", len(lines))
	}
	wantLevels := []string{"debug", "info", "warn", "error"}
	for i, line := range lines {
		m := decode(t, line)
		if m["level"] != wantLevels[i] {
			t.Fatalf("level = %v, want lowercase %s", m["level"], wantLevels[i])
		}
		if m["service"] != ServiceName {
			t.Fatalf("service = %v", m["service"])
		}
		if _, ok := m["msg"]; !ok {
			t.Fatal("msg missing")
		}
		ts, ok := m["time"].(string)
		if !ok {
			t.Fatal("time missing")
		}
		parsed, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			t.Fatalf("time %q is not RFC3339: %v", ts, err)
		}
		if parsed.Location() != time.UTC || !strings.HasSuffix(ts, "Z") {
			t.Fatalf("time %q is not UTC", ts)
		}
	}
	if m := decode(t, lines[1]); m["db"] != "alpha" {
		t.Fatalf("attribute lost: %v", m)
	}
}

func TestLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelWarn)
	log.Info("hidden")
	log.Warn("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Fatalf("output:\n%s", buf.String())
	}
}

func TestGroupedAttributesKeepTheirKeys(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo).Info("x", slog.Group("g", slog.String("level", "kept")))
	m := decode(t, strings.TrimSpace(buf.String()))
	g, _ := m["g"].(map[string]any)
	if g["level"] != "kept" {
		t.Fatalf("grouped level attr rewritten: %v", m)
	}
}
