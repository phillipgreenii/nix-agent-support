package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/queue"
)

const realQueuesPath = "../../../../claude-marketplace/pb/queues.json"

// validJSON returns a complete, valid configuration. Every path points at a
// location that does not exist: validation must never stat them.
func validFile(t *testing.T) File {
	t.Helper()
	raw, err := os.ReadFile(realQueuesPath)
	if err != nil {
		t.Fatalf("real queues file: %v", err)
	}
	var qs []QueueSpec
	if err := json.Unmarshal(raw, &qs); err != nil {
		t.Fatal(err)
	}
	return File{
		BDPath:                  "/nonexistent/store/bd/bin/bd",
		ChildPath:               "/nonexistent/store/bash/bin:/nonexistent/store/coreutils/bin",
		BeadsDirs:               map[string]string{"alpha": "/nonexistent/alpha/.beads", "beta": "/nonexistent/beta/.beads"},
		ClaudeDir:               "/nonexistent/claude",
		OperatorNames:           []string{"operator"},
		Port:                    9100,
		PollIntervalSeconds:     60,
		StrandedIntervalSeconds: 300,
		StaleClaimHours:         4,
		CommandTimeoutSeconds:   30,
		LabelCap:                20,
		Queues:                  qs,
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestValidConfigWithRealQueues(t *testing.T) {
	cfg, err := Parse(mustJSON(t, validFile(t)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := strings.Join(cfg.DBNames, ","); got != "alpha,beta" {
		t.Fatalf("DBNames = %s", got)
	}
	if len(cfg.ClassifiedQueues) != 4 {
		t.Fatalf("queues = %d, want 4", len(cfg.ClassifiedQueues))
	}
	for _, q := range cfg.ClassifiedQueues {
		if q.Class != queue.ClientSide {
			t.Fatalf("queue %s class = %s", q.Name, q.Class)
		}
	}
	if cfg.PollInterval() != 60*time.Second || cfg.StrandedInterval() != 300*time.Second || cfg.CommandTimeout() != 30*time.Second {
		t.Fatalf("durations: %v %v %v", cfg.PollInterval(), cfg.StrandedInterval(), cfg.CommandTimeout())
	}
}

func TestSpawnOnlyQueueIsAccepted(t *testing.T) {
	f := validFile(t)
	f.Queues = append(f.Queues, QueueSpec{Name: "by-priority", Args: []string{"--priority", "1"}})
	cfg, err := Validate(f)
	if err != nil {
		t.Fatal(err)
	}
	last := cfg.ClassifiedQueues[len(cfg.ClassifiedQueues)-1]
	if last.Class != queue.SpawnOnly {
		t.Fatalf("class = %s", last.Class)
	}
}

func TestRejectedQueueFailsConfigLoad(t *testing.T) {
	f := validFile(t)
	f.Queues = append(f.Queues, QueueSpec{Name: "bad", Args: []string{"--claim"}})
	_, err := Validate(f)
	if err == nil || !strings.Contains(err.Error(), "--claim") {
		t.Fatalf("err = %v, want rejection naming --claim", err)
	}
}

func TestEachFieldIsRequiredOrChecked(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *File)
		want   string
	}{
		{"bdPath empty", func(f *File) { f.BDPath = "" }, "bdPath is required"},
		{"bdPath relative", func(f *File) { f.BDPath = "bd" }, "bdPath must be an absolute path"},
		{"childPath empty", func(f *File) { f.ChildPath = "" }, "childPath is required"},
		{"childPath relative entry", func(f *File) { f.ChildPath = "/a/bin:bin" }, "childPath entries must be absolute"},
		{"childPath empty entry", func(f *File) { f.ChildPath = "/a/bin::/b/bin" }, "childPath entries must be absolute"},
		{"claudeDir empty", func(f *File) { f.ClaudeDir = "" }, "claudeDir is required"},
		{"claudeDir relative", func(f *File) { f.ClaudeDir = "claude" }, "claudeDir must be an absolute path"},
		{"no databases", func(f *File) { f.BeadsDirs = map[string]string{} }, "beadsDirs must name at least one"},
		{"bad db name", func(f *File) { f.BeadsDirs = map[string]string{"bad name": "/x/.beads"} }, "not a valid database name"},
		{"relative beadsDir", func(f *File) { f.BeadsDirs = map[string]string{"alpha": ".beads"} }, "must be an absolute path"},
		{"operatorNames absent", func(f *File) { f.OperatorNames = nil }, "operatorNames is required"},
		{"operatorNames blank", func(f *File) { f.OperatorNames = []string{" "} }, "non-empty"},
		{"operatorNames duplicate", func(f *File) { f.OperatorNames = []string{"a", "a"} }, "duplicated"},
		{"port zero", func(f *File) { f.Port = 0 }, "port must be between"},
		{"port high", func(f *File) { f.Port = 65536 }, "port must be between"},
		{"pollInterval", func(f *File) { f.PollIntervalSeconds = 0 }, "pollIntervalSeconds must be a positive"},
		{"strandedInterval", func(f *File) { f.StrandedIntervalSeconds = -1 }, "strandedIntervalSeconds must be a positive"},
		{"staleClaimHours", func(f *File) { f.StaleClaimHours = 0 }, "staleClaimHours must be a positive"},
		{"commandTimeout", func(f *File) { f.CommandTimeoutSeconds = 0 }, "commandTimeoutSeconds must be a positive"},
		{"labelCap", func(f *File) { f.LabelCap = 0 }, "labelCap must be a positive"},
		{"no queues", func(f *File) { f.Queues = nil }, "queues must name at least one"},
		{"queue bad name", func(f *File) { f.Queues = []QueueSpec{{Name: "Bad_Name", Args: []string{}}} }, "not a valid queue name"},
		{"queue duplicate", func(f *File) {
			f.Queues = []QueueSpec{{Name: "a", Args: []string{}}, {Name: "a", Args: []string{}}}
		}, "duplicated"},
		{"queue args absent", func(f *File) { f.Queues = []QueueSpec{{Name: "a"}} }, "args is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := validFile(t)
			tc.mutate(&f)
			_, err := Validate(f)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestAllProblemsReportedTogether(t *testing.T) {
	f := validFile(t)
	f.Port = 0
	f.LabelCap = 0
	_, err := Validate(f)
	if err == nil || !strings.Contains(err.Error(), "port must") || !strings.Contains(err.Error(), "labelCap must") {
		t.Fatalf("err = %v, want both problems", err)
	}
}

func TestParseRejectsUnknownAndTrailing(t *testing.T) {
	good := string(mustJSON(t, validFile(t)))
	cases := map[string]string{
		"unknown top-level field": strings.Replace(good, `"port":9100`, `"port":9100,"surprise":1`, 1),
		"unknown queue field":     strings.Replace(good, `{"name":"drain-claim"`, `{"extra":true,"name":"drain-claim"`, 1),
		"trailing data":           good + `{}`,
		"not json":                `nope`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestEmptyOperatorNamesListIsAccepted(t *testing.T) {
	f := validFile(t)
	f.OperatorNames = []string{}
	if _, err := Parse(mustJSON(t, f)); err != nil {
		t.Fatalf("empty operatorNames list: %v", err)
	}
}

func TestLoadReadsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, mustJSON(t, validFile(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// TestValidationNeverTouchesTheFilesystem makes the sandbox guarantee explicit:
// every path in the config is nonexistent and validation still succeeds.
func TestValidationNeverTouchesTheFilesystem(t *testing.T) {
	f := validFile(t)
	for _, p := range []string{f.BDPath, f.ClaudeDir, f.BeadsDirs["alpha"]} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("test premise broken: %s exists", p)
		}
	}
	if _, err := Validate(f); err != nil {
		t.Fatal(err)
	}
}
