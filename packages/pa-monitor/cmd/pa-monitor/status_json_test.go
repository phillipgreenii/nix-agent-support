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
