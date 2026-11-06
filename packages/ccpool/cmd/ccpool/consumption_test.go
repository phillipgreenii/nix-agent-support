package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

func seedConsumptionSession(t *testing.T, ext, transcript string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Insert(context.Background(), store.Session{ExternalID: ext, TranscriptPath: transcript}); err != nil {
		t.Fatal(err)
	}
}

func TestConsumption_jsonReading(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "t.jsonl")
	line := `{"type":"assistant","message":{"id":"m1","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":1000000,"output_tokens":1000000}}}` + "\n"
	if err := os.WriteFile(tp, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	seedConsumptionSession(t, "cons-a", tp)
	out, code := captureStdout(t, func() int { return runConsumption([]string{"cons-a", "--json"}) })
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var got struct {
		Model string `json:"model"`
		In    int    `json:"input_tokens"`
		Out   int    `json:"output_tokens"`
		Cents int64  `json:"estimated_cost_cents"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if got.Model != "claude-haiku-4-5-20251001" || got.In != 1000000 || got.Out != 1000000 || got.Cents != 600 {
		t.Errorf("got %+v", got)
	}
}

func TestConsumption_noTranscriptYetReadsZeros(t *testing.T) {
	seedConsumptionSession(t, "cons-b", "")
	out, code := captureStdout(t, func() int { return runConsumption([]string{"cons-b", "--json"}) })
	if code != 0 || !strings.Contains(out, `"estimated_cost_cents":0`) {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestConsumption_unknownSessionExits8(t *testing.T) {
	if _, code := captureStderr(t, func() int { return runConsumption([]string{"cons-nope"}) }); code != exitNoSuchSession {
		t.Fatalf("exit = %d, want %d", code, exitNoSuchSession)
	}
}

func TestConsumption_usageExits2(t *testing.T) {
	if _, code := captureStderr(t, func() int { return runConsumption(nil) }); code != 2 {
		t.Fatalf("exit = %d", code)
	}
}
