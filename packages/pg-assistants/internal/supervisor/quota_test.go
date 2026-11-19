package supervisor

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/quota"
)

// fakeQuota answers every read with fn's result and records the arguments.
type fakeQuota struct {
	fn    func(n int) (map[string]quota.Tokens, error)
	n     int
	dirs  []string
	since []time.Time
}

func (f *fakeQuota) Totals(_ context.Context, dir string, since time.Time) (map[string]quota.Tokens, error) {
	f.n++
	f.dirs, f.since = append(f.dirs, dir), append(f.since, since)
	return f.fn(f.n)
}

func tokensQuota(limit int64) *config.Quota {
	return &config.Quota{Tokens: limit, Window: time.Hour, Points: config.Points{Nudge: 0.8, Interrupt: 0.95, Kill: 1.0}}
}

func TestQuotaMeterNudgesAtItsPoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.a(exampleA).Quota = tokensQuota(1000)
		h.w.usage = usageTokens(1000) // context far below its 200000 limit
		fq := &fakeQuota{fn: func(int) (map[string]quota.Tokens, error) {
			return map[string]quota.Tokens{"claude-sonnet-5-5": {Input: 100, Output: 300, CacheCreation: 200, CacheRead: 250}}, nil
		}}
		h.quota = fq
		h.start()
		defer h.stop()
		h.until(90 * time.Second)

		st := h.statusOf(exampleA)
		if st.Rung != "nudge" || st.QuotaFraction != 0.85 || st.QuotaDetail != "850 of 1000 tokens in 1h0m0s" {
			t.Errorf("rung %q quota %v %q, want nudge at 0.85 with its detail", st.Rung, st.QuotaFraction, st.QuotaDetail)
		}
		wantReason := NudgeText("quota at 85% (850 of 1000 tokens in 1h0m0s)")
		if subs := h.w.callsOf("submit", exampleA); len(subs) != 1 || subs[0].text != wantReason {
			t.Errorf("submits = %+v, want one nudge %q", subs, wantReason)
		}
		if want := h.a(exampleA).Cwd; fq.dirs[0] != want {
			t.Errorf("queried dir %q, want the assistant's cwd %q", fq.dirs[0], want)
		}
		if got := fq.since[0].Add(time.Hour); !got.Equal(h.t0.Add(time.Minute)) {
			t.Errorf("window starts %v, want one hour before the first tick", fq.since[0])
		}
	})
}

func TestQuotaFailureSkipsTheMeterAndLogsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.a(exampleA).Quota = tokensQuota(1000)
		h.w.usage = usageTokens(1000)
		h.quota = &fakeQuota{fn: func(int) (map[string]quota.Tokens, error) { return nil, errors.New("recorder: down") }}
		h.start()
		defer h.stop()
		h.until(4 * time.Minute)

		if st := h.statusOf(exampleA); st.Rung != "" && st.Rung != "none" {
			t.Errorf("rung %q, want none: a failed quota read must not move the ladder", st.Rung)
		}
		if n := len(h.eventsFor("quota_failed", exampleA)); n != 1 {
			t.Errorf("quota_failed events = %d, want 1 (logged once while the error stays the same)", n)
		}
	})
}

func TestNoQuotaMeansNoRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.w.usage = usageTokens(1000)
		fq := &fakeQuota{fn: func(int) (map[string]quota.Tokens, error) { return nil, nil }}
		h.quota = fq
		h.start()
		defer h.stop()
		h.until(3 * time.Minute)
		if fq.n != 0 {
			t.Errorf("quota reads = %d, want 0 for an assistant without a quota", fq.n)
		}
	})
}
