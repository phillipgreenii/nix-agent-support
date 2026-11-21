package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/settle"
)

func seedLedger(t *testing.T, dir string) {
	t.Helper()
	l, err := settle.OpenLedger(filepath.Join(dir, "ack-ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, q := range []string{"qa", "qb"} {
		if err := l.Poll(q, "lane1", []settle.Item{{ID: q + "-1", Token: "t1"}}, now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLedgerDropOnlyTheNamedQueryAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, dir)
	var out, errb bytes.Buffer
	if code := ledgerDrop(&out, &errb, dir, "qa"); code != exitOK {
		t.Fatalf("exit = %d (%s)", code, errb.String())
	}
	l, err := settle.OpenLedger(filepath.Join(dir, "ack-ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Entries("qa", "lane1"); len(got) != 0 {
		t.Fatalf("dropped query still holds %v", got)
	}
	if got := l.Entries("qb", "lane1"); len(got) != 1 || got[0].ID != "qb-1" {
		t.Fatalf("other query changed: %v", got)
	}
}

func TestLedgerDropRefusedWhileRouterRuns(t *testing.T) {
	dir := t.TempDir()
	seedLedger(t, dir)
	held, err := eventqueue.LockLog(filepath.Join(dir, "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	var out, errb bytes.Buffer
	if code := ledgerDrop(&out, &errb, dir, "qa"); code != exitBusy {
		t.Fatalf("exit = %d, want busy", code)
	}
	if !strings.Contains(errb.String(), "nothing was changed") {
		t.Fatalf("stderr = %q", errb.String())
	}
	l, _ := settle.OpenLedger(filepath.Join(dir, "ack-ledger.json"))
	if len(l.Entries("qa", "lane1")) != 1 {
		t.Fatal("ledger changed despite refusal")
	}
}

func TestLedgerUsage(t *testing.T) {
	for _, a := range [][]string{nil, {"x"}, {"drop"}, {"drop", "a", "b"}} {
		if code := runLedger(a); code != exitUsage {
			t.Fatalf("%v: exit = %d", a, code)
		}
	}
}
