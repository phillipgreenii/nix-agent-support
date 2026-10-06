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
