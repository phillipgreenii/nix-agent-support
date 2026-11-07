package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestStatusTableAndJSON(t *testing.T) {
	root := t.TempDir()
	clk := fixedClock{time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	s := pool.NewStore(root, clk)
	for _, id := range []string{"a", "b"} {
		if err := s.CreateSlot(id); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.Acquire(pool.AcquireRequest{OwnerSessionID: "sess-1", BeadHint: "bead-x", Commit: "0123456789abcdef", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Unlock()

	var out, errb bytes.Buffer
	if code := run([]string{"status", "--root", root}, &out, &errb, clk); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"SLOT", "sess-1", "bead-x", "0123456789ab", "resetting", "warm-free"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table missing %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if code := run([]string{"status", "--root", root, "--json"}, &out, &errb, clk); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got []pool.SlotStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 2 {
		t.Fatalf("json: %v %s", err, out.String())
	}
	if !got[0].Held || got[0].Record.OwnerSessionID != "sess-1" || got[1].Held {
		t.Errorf("unexpected status: %+v", got)
	}
}

func TestStatusEmptyAndUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"status", "--root", t.TempDir(), "--json"}, &out, &errb, fixedClock{}); code != 0 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("empty: %d %q", code, out.String())
	}
	if code := run(nil, &out, &errb, fixedClock{}); code != 2 {
		t.Fatalf("no args exit %d", code)
	}
	if code := run([]string{"bogus"}, &out, &errb, fixedClock{}); code != 2 {
		t.Fatalf("bogus exit %d", code)
	}
}
