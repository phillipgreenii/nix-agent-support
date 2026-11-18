package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"testing"
)

func runChangesAckCmd(t *testing.T, entityType string, args ...string) (string, error) {
	t.Helper()
	c := newChangesAckCmd(entityType)
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err := c.ExecuteContext(context.Background())
	return out.String(), err
}

func ackFixture(t *testing.T) *changesFixture {
	f := newChangesFixture(t, "issue", "open")
	f.listing("open", 0, okSource, [2]string{"added", "bd-1"}, [2]string{"added", "bd-2"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	f.showIssue("bd-2", "2026-09-30T00:00:00Z")
	return f
}

func TestChangesNoAckLeavesCursorAndRedeliversSameItems(t *testing.T) {
	f := ackFixture(t)
	first, err := f.envOf("--consumer", "router", "--no-ack")
	if err != nil || len(first.Records) != 2 || first.Cursor.From != 0 || first.Cursor.To == 0 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	c, _ := f.consumer("router")
	if c.Cursor != 0 || c.Delivered != first.Cursor.To {
		t.Errorf("consumer after --no-ack = %+v, want cursor 0 delivered %d", c, first.Cursor.To)
	}
	again, err := f.envOf("--consumer", "router", "--no-ack")
	if err != nil || again.Cursor.From != 0 || again.Cursor.To != first.Cursor.To || len(again.Records) != 2 {
		t.Fatalf("redelivery = %+v, %v", again, err)
	}
	for i, r := range again.Records {
		if r.Seq != first.Records[i].Seq || r.ID != first.Records[i].ID {
			t.Errorf("redelivered record %d = %+v, want %+v", i, r, first.Records[i])
		}
	}
	// A cached peek still lists the records until the ack.
	peek, _ := f.envOf("--consumer", "router", "--cached")
	if len(peek.Records) != 2 {
		t.Errorf("cached before ack lists %d records", len(peek.Records))
	}
	out, err := runChangesAckCmd(t, "issue", "--consumer", "router", "--next-seq", strconv.FormatInt(first.Cursor.To, 10))
	if err != nil {
		t.Fatalf("changes-ack: %v", err)
	}
	var got changesAckResult
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil || got != (changesAckResult{Consumer: "router", Type: "issue", AckedSeq: first.Cursor.To}) {
		t.Errorf("answer = %q (%v)", out, jerr)
	}
	peek, _ = f.envOf("--consumer", "router", "--cached")
	if len(peek.Records) != 0 {
		t.Errorf("cached after ack lists %d records", len(peek.Records))
	}
}

func TestChangesAckIsMonotonicAndBounded(t *testing.T) {
	f := ackFixture(t)
	env, err := f.envOf("--consumer", "router", "--no-ack")
	if err != nil {
		t.Fatal(err)
	}
	mid := env.Records[0].Seq
	ack := func(n int64) (changesAckResult, error) {
		out, err := runChangesAckCmd(t, "issue", "--consumer", "router", "--next-seq", strconv.FormatInt(n, 10))
		var r changesAckResult
		if err == nil {
			if jerr := json.Unmarshal([]byte(out), &r); jerr != nil {
				t.Fatalf("answer %q: %v", out, jerr)
			}
		}
		return r, err
	}
	if r, err := ack(mid); err != nil || r.AckedSeq != mid {
		t.Fatalf("ack mid = %+v, %v", r, err)
	}
	// Lower and repeated values are harmless no-ops that report the cursor.
	for _, n := range []int64{mid, 0, mid - 1} {
		if r, err := ack(n); err != nil || r.AckedSeq != mid {
			t.Errorf("ack %d = %+v, %v; want no-op at %d", n, r, err, mid)
		}
	}
	if c, _ := f.consumer("router"); c.Cursor != mid {
		t.Errorf("cursor = %d, want %d", c.Cursor, mid)
	}
	// Past the highest delivered: usage error, cursor unchanged.
	if _, err := ack(env.Cursor.To + 1); exitCodeFor(err) != 2 {
		t.Errorf("above delivered: err = %v, code %d", err, exitCodeFor(err))
	}
	if c, _ := f.consumer("router"); c.Cursor != mid {
		t.Errorf("cursor moved on a refused ack: %d", c.Cursor)
	}
	if r, err := ack(env.Cursor.To); err != nil || r.AckedSeq != env.Cursor.To {
		t.Errorf("ack to delivered = %+v, %v", r, err)
	}
}

func TestChangesAckExitCodes(t *testing.T) {
	f := ackFixture(t)
	if _, err := f.envOf("--consumer", "router", "--no-ack"); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args []string
		code int
	}{
		"unregistered":                 {[]string{"--consumer", "ghost", "--next-seq", "0"}, 4},
		"unregistered and too high":    {[]string{"--consumer", "ghost", "--next-seq", "999999"}, 4},
		"non-integer":                  {[]string{"--consumer", "router", "--next-seq", "abc"}, 2},
		"negative":                     {[]string{"--consumer", "router", "--next-seq", "-1"}, 2},
		"missing next-seq":             {[]string{"--consumer", "router"}, 2},
		"missing consumer":             {[]string{"--next-seq", "1"}, 2},
		"above delivered":              {[]string{"--consumer", "router", "--next-seq", "999999"}, 2},
		"malformed beats missing user": {[]string{"--consumer", "ghost", "--next-seq", "x"}, 2},
	} {
		_, err := runChangesAckCmd(t, "issue", tc.args...)
		if err == nil || exitCodeFor(err) != tc.code {
			t.Errorf("%s: err = %v, code %d, want %d", name, err, exitCodeFor(err), tc.code)
		}
	}
	if _, ok := f.consumer("ghost"); ok {
		t.Error("changes-ack registered the consumer")
	}
	out, err := runChangesAckCmd(t, "issue", "--consumer", "router", "--next-seq", "0", "--output", "human")
	if err != nil || out != "acknowledged consumer \"router\" at position 0\n" {
		t.Errorf("human = %q, %v", out, err)
	}
}

func TestChangesDefaultCallStillAdvancesAtEmitAndRaisesDelivered(t *testing.T) {
	f := ackFixture(t)
	env, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := f.consumer("router")
	if c.Cursor != env.Cursor.To || c.Delivered != env.Cursor.To {
		t.Errorf("consumer = %+v, want cursor and delivered %d", c, env.Cursor.To)
	}
	// An empty poll never lowers delivered; a later --no-ack never lowers it.
	if _, err := f.envOf("--consumer", "router", "--no-ack"); err != nil {
		t.Fatal(err)
	}
	if c2, _ := f.consumer("router"); c2.Delivered != env.Cursor.To || c2.Cursor != env.Cursor.To {
		t.Errorf("after empty --no-ack = %+v", c2)
	}
}
