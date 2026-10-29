package main

import (
	"database/sql"
	"strconv"
	"testing"
	"time"
)

type settleCase struct {
	name       string
	check      string // tracker.check decision, "" for none
	mode       string
	cetaEngine string // ceta.verdict verdict, "" for none
	cetaDeliv  string
	outcome    string
	wantBy     string
	wantSettle string
	wantConv   int
}

// feed writes one call per case into its own session and returns the session id.
func (a *app) feed(c settleCase, i int, ts int64) string {
	sess := "settle" + strconv.Itoa(i)
	tu := "tu" + strconv.Itoa(i)
	n := func(d int64) string { return strconv.FormatInt(ts+d, 10) }
	a.append(sess, "tracker", "tracker.call_start", tu, n(0), tu+"-1", `{"tool":"Bash","input":{}}`)
	if c.check != "" {
		a.append(sess, "tracker", "tracker.check", tu, n(1), tu+"-2", `{"decision":"`+c.check+`"}`)
	}
	if c.mode != "" {
		a.append(sess, "tracker", "tracker.pre_tool", tu, n(2), tu+"-3", `{"tool_name":"Bash","permission_mode":"`+c.mode+`"}`)
	}
	if c.cetaEngine != "" {
		a.append(sess, "ceta", "ceta.verdict", tu, n(1), tu+"-4", `{"verdict":"`+c.cetaEngine+`","delivered":"`+c.cetaDeliv+`","sandbox_enabled":false}`)
	}
	if c.outcome != "" {
		a.append(sess, "tracker", "tracker.call_end", tu, n(5), tu+"-5", `{"outcome":"`+c.outcome+`"}`)
	}
	return sess
}

func settleOf(t *testing.T, db *sql.DB, tu string) (by, st sql.NullString, conv int, failed sql.NullInt64) {
	t.Helper()
	if err := db.QueryRow("SELECT settled_by, settlement, converted_ask, failed_timing FROM tool_calls WHERE tool_use_id=?", tu).
		Scan(&by, &st, &conv, &failed); err != nil {
		t.Fatal(err)
	}
	return
}

func TestSettlementTable(t *testing.T) {
	cases := []settleCase{
		{name: "ceta allow", check: "allow", cetaEngine: "approve", cetaDeliv: "approve", outcome: "ok", wantBy: "ceta", wantSettle: "approved"},
		{name: "ceta deny", check: "deny", cetaEngine: "reject", cetaDeliv: "reject", outcome: "refused", wantBy: "ceta", wantSettle: "refused"},
		{name: "ceta ask approved", check: "ask", cetaEngine: "ask", cetaDeliv: "ask", outcome: "ok", wantBy: "person", wantSettle: "approved"},
		{name: "ceta ask refused", check: "ask", cetaEngine: "ask", cetaDeliv: "ask", outcome: "refused", wantBy: "person", wantSettle: "refused"},
		{name: "allow bypass", check: "allow", mode: "bypassPermissions", outcome: "ok", wantBy: "mode", wantSettle: "approved"},
		{name: "allow other", check: "allow", mode: "default", outcome: "ok", wantBy: "rule", wantSettle: "approved"},
		{name: "deny rule", check: "deny", mode: "default", outcome: "refused", wantBy: "rule", wantSettle: "refused"},
		{name: "ask auto ran", check: "ask", mode: "auto", outcome: "ok", wantBy: "classifier", wantSettle: "approved"},
		{name: "ask auto refused", check: "ask", mode: "auto", outcome: "refused", wantBy: "classifier", wantSettle: "refused"},
		{name: "ask default ran", check: "ask", mode: "default", outcome: "ok", wantBy: "person", wantSettle: "approved"},
		{name: "ask acceptEdits interrupted", check: "ask", mode: "acceptEdits", outcome: "interrupted", wantBy: "person", wantSettle: "refused"},
		{name: "ask dontAsk", check: "ask", mode: "dontAsk", outcome: "refused", wantBy: "mode", wantSettle: "refused"},
		{name: "converted ask", check: "ask", mode: "default", cetaEngine: "ask", cetaDeliv: "reject", outcome: "refused", wantBy: "ceta", wantSettle: "refused", wantConv: 1},
	}
	a := newApp(t)
	base := time.Now().UnixMilli() - 1000
	for i, c := range cases {
		a.feed(c, i, base)
	}
	a.ingest()
	db := a.open()
	for i, c := range cases {
		by, st, conv, _ := settleOf(t, db, "tu"+strconv.Itoa(i))
		if by.String != c.wantBy || st.String != c.wantSettle || conv != c.wantConv {
			t.Errorf("%s: got by=%q settlement=%q converted=%d, want %q %q %d", c.name, by.String, st.String, conv, c.wantBy, c.wantSettle, c.wantConv)
		}
	}
}

func TestSettlementPendingThenUnresolvedBySessionEnd(t *testing.T) {
	a := newApp(t)
	now := time.Now().UnixMilli() - 1000
	c := settleCase{check: "allow", mode: "default"}
	a.feed(c, 1, now)
	a.ingest()
	by, st, _, _ := settleOf(t, a.open(), "tu1")
	if by.String != "rule" || st.String != "pending" {
		t.Fatalf("live session: %q %q, want rule pending", by.String, st.String)
	}
	a.append("settle1", "tracker", "tracker.session_end", "", strconv.FormatInt(now+100, 10), "se-1", `{"reason":"other","normal":false}`)
	a.ingest()
	if _, st, _, _ := settleOf(t, a.open(), "tu1"); st.String != "unresolved" {
		t.Errorf("after session_end: %q, want unresolved", st.String)
	}
}

func TestSettlementUnresolvedByQuietPeriod(t *testing.T) {
	a := newApp(t)
	old := time.Now().Add(-3 * time.Hour).UnixMilli()
	a.feed(settleCase{check: "ask", mode: "default"}, 1, old)
	a.ingest()
	by, st, _, _ := settleOf(t, a.open(), "tu1")
	if by.String != "person" || st.String != "unresolved" {
		t.Errorf("quiet session: %q %q, want person unresolved", by.String, st.String)
	}
}

func TestSettlementRecomputedForResumedSessionKeepsCallID(t *testing.T) {
	a := newApp(t)
	old := time.Now().Add(-3 * time.Hour).UnixMilli()
	a.feed(settleCase{check: "ask", mode: "default"}, 1, old)
	a.ingest()
	db := a.open()
	id := scalar[int](t, db, "SELECT call_id FROM tool_calls WHERE tool_use_id='tu1'")
	if _, st, _, _ := settleOf(t, db, "tu1"); st.String != "unresolved" {
		t.Fatalf("before resume: %q", st.String)
	}
	a.append("settle1", "tracker", "tracker.call_end", "tu1", strconv.FormatInt(time.Now().UnixMilli(), 10), "late-1", `{"outcome":"ok"}`)
	a.ingest()
	db2 := a.open()
	if _, st, _, _ := settleOf(t, db2, "tu1"); st.String != "approved" {
		t.Errorf("after resume: %q, want approved", st.String)
	}
	if got := scalar[int](t, db2, "SELECT call_id FROM tool_calls WHERE tool_use_id='tu1'"); got != id {
		t.Errorf("call_id changed %d -> %d", id, got)
	}
}

func TestSettlementErroredCallApprovedAndFailedForTiming(t *testing.T) {
	a := newApp(t)
	now := time.Now().UnixMilli() - 1000
	a.feed(settleCase{check: "allow", mode: "default", outcome: "error"}, 1, now)
	a.feed(settleCase{check: "allow", mode: "default", outcome: "ok"}, 2, now)
	a.ingest()
	db := a.open()
	by, st, _, failed := settleOf(t, db, "tu1")
	if by.String != "rule" || st.String != "approved" || !failed.Valid || failed.Int64 != 1 {
		t.Errorf("errored call: %q %q failed=%v", by.String, st.String, failed)
	}
	if _, _, _, f := settleOf(t, db, "tu2"); !f.Valid || f.Int64 != 0 {
		t.Errorf("ok call failed_timing = %v, want 0", f)
	}
}
