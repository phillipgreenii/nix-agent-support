package status

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func p64(v int64) *int64 { return &v }
func pb(v bool) *bool    { return &v }

func (r *rig) withReading(rl internal.RateLimit, at time.Time) {
	rs := origin.NewReadings()
	rs.Record(rl, at)
	r.cfg.Readings = rs
}

func TestRateLimitBlock_FloorAloneWhenNoReadingExists(t *testing.T) {
	r := newRig(t)
	rep := r.collect()
	if rep.RateLimit == nil || rep.RateLimit.Floor != 20 {
		t.Fatalf("rate_limit = %+v, want the configured floor 20", rep.RateLimit)
	}
	rl := rep.RateLimit
	if rl.Limit != nil || rl.Remaining != nil || rl.NearLimit != nil || rl.Headroom != nil || rl.ReadingAgeSeconds != nil || rl.BelowFloor {
		t.Errorf("no reading means nothing but the floor is stated: %+v", rl)
	}
	// A holder with no reading in it is the same.
	r.cfg.Readings = origin.NewReadings()
	if rl := r.collect().RateLimit; rl.Remaining != nil || rl.Floor != 20 {
		t.Errorf("empty holder: %+v", rl)
	}
	if rep.Health != Healthy {
		t.Errorf("a missing reading is not a degradation: %s %v", rep.Health, rep.Reasons)
	}
}

func TestRateLimitBlock_ACurrentReadingAboveTheFloorIsHealthy(t *testing.T) {
	r := newRig(t)
	r.withReading(internal.RateLimit{Status: 200, Limit: p64(100), Remaining: p64(97), NearLimit: pb(false)}, t0.Add(-30*time.Second))
	rep := r.collect()
	rl := rep.RateLimit
	if rl.Floor != 20 || *rl.Limit != 100 || *rl.Remaining != 97 || *rl.NearLimit || *rl.Headroom != 77 || rl.BelowFloor ||
		rl.LastStatus != 200 || *rl.ReadingAgeSeconds != 30 || rl.ResetAt != nil {
		t.Fatalf("rate_limit = %+v", rl)
	}
	if rep.Health != Healthy || rep.Exit() != 0 {
		t.Errorf("health = %s %v", rep.Health, rep.Reasons)
	}
}

func TestRateLimitBlock_BelowTheFloorIsDegradedAndNamed(t *testing.T) {
	r := newRig(t)
	r.withReading(internal.RateLimit{
		Status: 429, Limit: p64(100), Remaining: p64(4), NearLimit: pb(true),
		RetryAfterSeconds: 11, Reset: "2026-10-24T12:00:11Z", Reason: "jira-burst-based",
	}, t0.Add(-5*time.Second))
	rep := r.collect()
	rl := rep.RateLimit
	if !rl.BelowFloor || *rl.Headroom != -16 || rl.Reason != "jira-burst-based" || rl.ResetAt == nil || !rl.ResetAt.Equal(time.Date(2026, 10, 24, 12, 0, 11, 0, time.UTC)) {
		t.Fatalf("rate_limit = %+v", rl)
	}
	if rep.Health != Degraded || rep.Exit() != 2 {
		t.Fatalf("health = %s", rep.Health)
	}
	if !hasReason(rep, "below the floor", "4 requests remaining", "floor 20", "5s old") {
		t.Errorf("reasons = %v", rep.Reasons)
	}
}

func TestRateLimitBlock_AFloorOfExactlyTheRemainingIsNotBelow(t *testing.T) {
	r := newRig(t)
	r.withReading(internal.RateLimit{Status: 200, Limit: p64(100), Remaining: p64(20)}, t0)
	if rl := r.collect().RateLimit; rl.BelowFloor || *rl.Headroom != 0 {
		t.Errorf("remaining equal to the floor: %+v", rl)
	}
}

func TestRateLimitBlock_AnOldReadingIsNotReportedOrJudged(t *testing.T) {
	r := newRig(t)
	r.withReading(internal.RateLimit{Status: 200, Limit: p64(100), Remaining: p64(1)}, t0.Add(-origin.ReadingMaxAge-time.Second))
	rep := r.collect()
	if rep.RateLimit.Remaining != nil || rep.RateLimit.BelowFloor || rep.Health != Healthy {
		t.Errorf("a reading past ReadingMaxAge must drop out: %+v health %s", rep.RateLimit, rep.Health)
	}
}

func TestRateLimitBlock_AReadingWithoutRemainingStatesOnlyWhatItCarries(t *testing.T) {
	r := newRig(t)
	r.withReading(internal.RateLimit{Status: 200, Limit: p64(100), NearLimit: pb(false)}, t0)
	rl := r.collect().RateLimit
	if rl.Remaining != nil || rl.Headroom != nil || rl.BelowFloor || *rl.Limit != 100 {
		t.Errorf("rate_limit = %+v", rl)
	}
	raw, _ := json.Marshal(rl)
	if strings.Contains(string(raw), `"remaining"`) || strings.Contains(string(raw), `"headroom"`) {
		t.Errorf("an absent key must be absent from the JSON, not 0: %s", raw)
	}
}

func TestRateLimitBlock_TheFloorFollowsTheConfig(t *testing.T) {
	r := newRig(t)
	r.dcfg.RateReserveRequests = 0
	r.withReading(internal.RateLimit{Status: 200, Remaining: p64(1)}, t0)
	rl := r.collect().RateLimit
	if rl.Floor != 0 || rl.BelowFloor || *rl.Headroom != 1 {
		t.Errorf("rate_limit = %+v", rl)
	}
}

// The op serves the block over the router like the rest of the report, and the
// text form states the same values.
func TestRateLimitBlock_ServedByTheStatusOpAndRendered(t *testing.T) {
	r := newRig(t)
	r.withReading(internal.RateLimit{Status: 200, Limit: p64(100), Remaining: p64(50), NearLimit: pb(false)}, t0.Add(-2*time.Second))
	rt := router.New(router.Options{})
	if err := Register(rt, r.cfg); err != nil {
		t.Fatal(err)
	}
	resp := rt.Dispatch(context.Background(), router.Call{Instance: "issue", Request: scriptout.Request{Op: "status"}})
	if resp.Error != nil || resp.SchemaVersion != schema.IssueSchemaVersion {
		t.Fatalf("resp = %+v", resp)
	}
	var rep Report
	if err := json.Unmarshal(resp.Result, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.RateLimit == nil || *rep.RateLimit.Remaining != 50 || rep.RateLimit.Floor != 20 {
		t.Fatalf("rate_limit over the wire = %+v", rep.RateLimit)
	}
	var text bytes.Buffer
	if err := Render(&text, rep); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rate_limit: floor=20", "remaining=50 headroom=30", "limit=100", "near_limit=false", "reading_age_seconds=2"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, text.String())
		}
	}
}
