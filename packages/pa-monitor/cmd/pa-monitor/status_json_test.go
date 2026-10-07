package main

import (
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

func TestStatusJSON_SessionFields(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	state := &pb.DaemonState{
		Dirs: []*pb.Directory{{
			Sessions: []*pb.SessionView{{
				SessionId:     "s1",
				Pid:           4567,
				Cwd:           "/repo",
				Model:         "claude-sonnet-5",
				Status:        "blocked",
				Blocker:       "usage_limit",
				SessionTokens: 12345,
				CostUsd:       1.23,
				StartedAt:     timestamppb.New(now.Add(-time.Hour)),
			}},
		}},
		ActiveBlock: &pb.Block{Id: "b1", CostUsd: 4.5},
	}
	details := []*pb.SessionDetail{{View: state.Dirs[0].Sessions[0]}}

	doc := statusJSON(state, details, now)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed struct {
		Sessions []struct {
			SessionID     string  `json:"session_id"`
			Pid           int     `json:"pid"`
			Status        string  `json:"status"`
			Blocker       string  `json:"blocker"`
			SessionTokens uint64  `json:"session_tokens"`
			CostUSD       float64 `json:"cost_usd"`
			LongIdle      bool    `json:"long_idle"`
		} `json:"sessions"`
		ActiveBlock *struct {
			ID      string  `json:"id"`
			CostUSD float64 `json:"cost_usd"`
		} `json:"active_block"`
		ActiveWeek *struct{} `json:"active_week"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(parsed.Sessions))
	}
	s := parsed.Sessions[0]
	if s.SessionID != "s1" || s.Pid != 4567 || s.Status != "blocked" || s.Blocker != "usage_limit" {
		t.Errorf("session fields wrong: %+v", s)
	}
	if s.SessionTokens != 12345 || s.CostUSD != 1.23 {
		t.Errorf("usage fields wrong: %+v", s)
	}
	if parsed.ActiveBlock == nil || parsed.ActiveBlock.ID != "b1" {
		t.Errorf("active_block wrong: %+v", parsed.ActiveBlock)
	}
	if parsed.ActiveWeek != nil {
		t.Errorf("active_week should be nil, got %+v", parsed.ActiveWeek)
	}
}

func TestStatusJSON_DeadPidOmitted(t *testing.T) {
	now := time.Now().UTC()
	state := &pb.DaemonState{
		Dirs: []*pb.Directory{{Sessions: []*pb.SessionView{{SessionId: "s2", Pid: 0}}}},
	}
	doc := statusJSON(state, nil, now)
	raw, _ := json.Marshal(doc)
	var parsed struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal(raw, &parsed)
	if _, ok := parsed.Sessions[0]["pid"]; ok {
		t.Errorf("pid should be omitted for a dead session, got %v", parsed.Sessions[0]["pid"])
	}
}

func TestStatusJSON_RateLimits(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	five, seven := 100.0, 42.5
	state := &pb.DaemonState{
		FiveHourPct:      &five,
		FiveHourResetsAt: timestamppb.New(now.Add(2 * time.Hour)),
		SevenDayPct:      &seven, // percentage known, reset unknown
		LimitsCapturedAt: timestamppb.New(now.Add(-time.Minute)),
	}
	raw, err := json.Marshal(statusJSON(state, nil, now))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed struct {
		RateLimits *struct {
			FiveHour *struct {
				UsedPct  *float64 `json:"used_pct"`
				ResetsAt *string  `json:"resets_at"`
			} `json:"five_hour"`
			SevenDay *struct {
				UsedPct  *float64 `json:"used_pct"`
				ResetsAt *string  `json:"resets_at"`
			} `json:"seven_day"`
			CapturedAt *string `json:"captured_at"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rl := parsed.RateLimits
	if rl == nil || rl.FiveHour == nil || rl.SevenDay == nil {
		t.Fatalf("rate_limits missing windows: %s", raw)
	}
	if rl.FiveHour.UsedPct == nil || *rl.FiveHour.UsedPct != 100 {
		t.Errorf("five_hour.used_pct = %v, want 100", rl.FiveHour.UsedPct)
	}
	if rl.FiveHour.ResetsAt == nil || *rl.FiveHour.ResetsAt != "2026-10-06T14:00:00Z" {
		t.Errorf("five_hour.resets_at = %v, want 2026-10-06T14:00:00Z", rl.FiveHour.ResetsAt)
	}
	if rl.SevenDay.UsedPct == nil || *rl.SevenDay.UsedPct != 42.5 {
		t.Errorf("seven_day.used_pct = %v, want 42.5", rl.SevenDay.UsedPct)
	}
	if rl.SevenDay.ResetsAt != nil {
		t.Errorf("seven_day.resets_at must be absent when unknown, got %q", *rl.SevenDay.ResetsAt)
	}
	if rl.CapturedAt == nil || *rl.CapturedAt != "2026-10-06T11:59:00Z" {
		t.Errorf("captured_at = %v, want 2026-10-06T11:59:00Z", rl.CapturedAt)
	}
}

func TestStatusJSON_RateLimitsOmittedWhenUnknown(t *testing.T) {
	raw, err := json.Marshal(statusJSON(&pb.DaemonState{}, nil, time.Now().UTC()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := parsed["rate_limits"]; ok {
		t.Errorf("rate_limits must be omitted when no window is known, got %v", parsed["rate_limits"])
	}
}

func TestCaffeinateProcessToken(t *testing.T) {
	cases := []struct {
		in   pb.CaffeinateProcess
		want string
	}{
		{pb.CaffeinateProcess_CAFFEINATE_PROCESS_OFF, "off"},
		{pb.CaffeinateProcess_CAFFEINATE_PROCESS_ON, "holding"},
		{pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE, "grace"},
		{pb.CaffeinateProcess_CAFFEINATE_PROCESS_ERROR, "error"},
		{pb.CaffeinateProcess(99), "unknown"},
		{pb.CaffeinateProcess(-1), "unknown"},
	}
	for _, c := range cases {
		if got := caffeinateProcessToken(c.in); got != c.want {
			t.Errorf("caffeinateProcessToken(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// statusJSONMap marshals the status document and decodes it generically so a
// test can distinguish a key that is absent from one that is false/zero.
func statusJSONMap(t *testing.T, state *pb.DaemonState) map[string]any {
	t.Helper()
	raw, err := json.Marshal(statusJSON(state, nil, time.Now().UTC()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestStatusJSON_Caffeinate(t *testing.T) {
	cases := []struct {
		name        string
		mode        bool
		process     pb.CaffeinateProcess
		graceS      uint32
		wantProcess string
		wantGrace   bool
	}{
		{"mode on holding", true, pb.CaffeinateProcess_CAFFEINATE_PROCESS_ON, 0, "holding", false},
		{"mode off off", false, pb.CaffeinateProcess_CAFFEINATE_PROCESS_OFF, 0, "off", false},
		{"mode on grace", true, pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE, 42, "grace", true},
		{"mode off grace", false, pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE, 7, "grace", true},
		{"mode on error", true, pb.CaffeinateProcess_CAFFEINATE_PROCESS_ERROR, 0, "error", false},
		{"out of range", false, pb.CaffeinateProcess(99), 0, "unknown", false},
		// grace seconds are only meaningful during grace: stray non-zero
		// seconds in any other process state are not emitted.
		{"stray grace seconds outside grace", true, pb.CaffeinateProcess_CAFFEINATE_PROCESS_ON, 30, "holding", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := statusJSONMap(t, &pb.DaemonState{
				CaffeinateMode:            c.mode,
				CaffeinateProcess:         c.process,
				CaffeinateGraceRemainingS: c.graceS,
			})
			caf, ok := m["caffeinate"].(map[string]any)
			if !ok {
				t.Fatalf("caffeinate key missing or wrong type: %v", m["caffeinate"])
			}
			if caf["mode"] != c.mode {
				t.Errorf("mode = %v, want %v", caf["mode"], c.mode)
			}
			if caf["process"] != c.wantProcess {
				t.Errorf("process = %v, want %q", caf["process"], c.wantProcess)
			}
			g, hasGrace := caf["grace_remaining_s"]
			if hasGrace != c.wantGrace {
				t.Fatalf("grace_remaining_s present = %v, want %v (%v)", hasGrace, c.wantGrace, caf)
			}
			if c.wantGrace && g != float64(c.graceS) {
				t.Errorf("grace_remaining_s = %v, want %d", g, c.graceS)
			}
		})
	}
}

func TestStatusJSON_GraceZeroOmitted(t *testing.T) {
	// omitempty: a grace state with 0 seconds left carries no key; consumers
	// MUST read the absence as 0.
	m := statusJSONMap(t, &pb.DaemonState{
		CaffeinateMode:    true,
		CaffeinateProcess: pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE,
	})
	caf := m["caffeinate"].(map[string]any)
	if _, ok := caf["grace_remaining_s"]; ok {
		t.Errorf("grace_remaining_s must be omitted when 0, got %v", caf)
	}
	if caf["process"] != "grace" {
		t.Errorf("process = %v, want grace", caf["process"])
	}
}

func TestStatusJSON_AutoResumeAlwaysPresent(t *testing.T) {
	for _, want := range []bool{true, false} {
		m := statusJSONMap(t, &pb.DaemonState{AutoResumeEnabled: want})
		got, ok := m["auto_resume"]
		if !ok {
			t.Fatalf("auto_resume must be present even when %v", want)
		}
		if got != want {
			t.Errorf("auto_resume = %v, want %v", got, want)
		}
		if _, ok := m["caffeinate"]; !ok {
			t.Errorf("caffeinate must always be emitted by a new client")
		}
	}
}

// TestStatusJSON_OldShapeDecodes pins backward compatibility: a document
// WITHOUT the new keys (an older pa-monitor client's output) still decodes
// into the current struct, leaving the new fields at their zero values.
func TestStatusJSON_OldShapeDecodes(t *testing.T) {
	old := `{"sessions":[{"session_id":"s1","cwd":"/r","model":"m","status":"idle","session_tokens":1,"cost_usd":0,"long_idle":false}],"active_block":{"id":"b1","cost_usd":1}}`
	var doc statusJSONDoc
	if err := json.Unmarshal([]byte(old), &doc); err != nil {
		t.Fatalf("old-shape decode: %v", err)
	}
	if len(doc.Sessions) != 1 || doc.Sessions[0].SessionID != "s1" {
		t.Errorf("sessions decoded wrong: %+v", doc.Sessions)
	}
	if doc.Caffeinate != nil || doc.AutoResume {
		t.Errorf("new fields must stay zero for an old doc: %+v %v", doc.Caffeinate, doc.AutoResume)
	}
}

func TestStripJSONFlag(t *testing.T) {
	rest, jsonMode := stripJSONFlag([]string{"session:s1", "--json"})
	if !jsonMode || len(rest) != 1 || rest[0] != "session:s1" {
		t.Errorf("got rest=%v jsonMode=%v", rest, jsonMode)
	}
	rest, jsonMode = stripJSONFlag([]string{"--json", "session:s1"})
	if !jsonMode || len(rest) != 1 || rest[0] != "session:s1" {
		t.Errorf("--json before the positional arg: got rest=%v jsonMode=%v", rest, jsonMode)
	}
	rest, jsonMode = stripJSONFlag([]string{"session:s1"})
	if jsonMode || len(rest) != 1 {
		t.Errorf("no flag present: got rest=%v jsonMode=%v", rest, jsonMode)
	}
}
