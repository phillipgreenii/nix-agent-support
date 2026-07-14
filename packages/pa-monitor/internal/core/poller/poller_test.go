package poller

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	claudetranscript "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// makeRateLimitFixture writes a session file + transcript with a single
// synthetic rate-limit event whose reset time is the supplied resetISO. Returns
// (sessionsDir, claudeHome).
func makeRateLimitFixture(t *testing.T, resetISO string) (string, string) {
	t.Helper()
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	claudeHome := filepath.Join(root, "claude-home")
	cwd := filepath.Join(root, "cwd")
	slug := strings.NewReplacer("/", "-", "_", "-").Replace(cwd)
	projectDir := filepath.Join(claudeHome, "projects", slug)
	for _, d := range []string{sessionsDir, projectDir, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionJSON := fmt.Sprintf(`{"pid":99001,"sessionId":"rl-sess","cwd":%q,"startedAt":1776000000000,"kind":"interactive","entrypoint":"cli"}`, cwd)
	if err := os.WriteFile(filepath.Join(sessionsDir, "99001.json"), []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// The encoded project dir name is "-<basename>-cwd" produced from cwd above.
	transcript := `{"type":"assistant","timestamp":"` + resetISO + `","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your limit · resets 1pm (UTC)"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, "rl-sess.jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	return sessionsDir, claudeHome
}

func TestSnapshotZeroesStaleRateLimitResetsAt(t *testing.T) {
	// Synthetic rate-limit event written 2026-05-05 at 00:00 UTC says
	// "resets 1pm (UTC)" → reset = 2026-05-05 13:00 UTC. With Now() far past that
	// (2026-05-06), the enrichment must show RateLimitResetsAt zero.
	sessionsDir, claudeHome := makeRateLimitFixture(t, "2026-05-05T00:00:00Z")
	p := &Poller{
		SessionsDir: sessionsDir,
		ClaudeHome:  claudeHome,
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC) },
		Pricer:      &fakeCostPricer{},
	}
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range tree.Dirs {
		for _, s := range d.Sessions {
			if !s.RateLimitResetsAt.IsZero() {
				t.Errorf("RateLimitResetsAt = %v, want zero (stale beyond grace)", s.RateLimitResetsAt)
			}
		}
	}
	if !tree.WindowResetsAt.IsZero() {
		t.Errorf("WindowResetsAt = %v, want zero (no live pauses)", tree.WindowResetsAt)
	}
}

func TestSnapshotKeepsRecentRateLimitResetsAt(t *testing.T) {
	// Reset at 2026-05-05 13:00 UTC, Now() = 2026-05-05 13:02 UTC (only 2 min
	// past — within stalePauseGrace). The reset MUST be preserved so the auto-
	// resume path can still fire.
	sessionsDir, claudeHome := makeRateLimitFixture(t, "2026-05-05T00:00:00Z")
	p := &Poller{
		SessionsDir: sessionsDir,
		ClaudeHome:  claudeHome,
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Date(2026, 5, 5, 13, 2, 0, 0, time.UTC) },
		Pricer:      &fakeCostPricer{},
	}
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 5, 5, 13, 0, 0, 0, time.UTC)
	found := false
	for _, d := range tree.Dirs {
		for _, s := range d.Sessions {
			if s.RateLimitResetsAt.Equal(want) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("RateLimitResetsAt was filtered prematurely; tree=%+v", tree.Dirs)
	}
}

func TestSnapshotProducesTree(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../../tests/fixtures/sessions",
		ClaudeHome:  "../../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.Pricer = &fakeCostPricer{}
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tree == nil {
		t.Fatal("nil tree")
	}
}

func TestSnapshotEnrichmentFields(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../../tests/fixtures/sessions",
		ClaudeHome:  "../../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.Pricer = &fakeCostPricer{}
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Find the session with cwd "/tmp/x" (abc-def)
	var found *struct {
		sessionTokens int
		contextTokens int
		model         string
	}
	for _, d := range tree.Dirs {
		if d.Path != "/tmp/x" {
			continue
		}
		for _, s := range d.Sessions {
			found = &struct {
				sessionTokens int
				contextTokens int
				model         string
			}{
				sessionTokens: s.SessionTokens,
				contextTokens: s.ContextTokens,
				model:         s.Model,
			}
		}
	}
	if found == nil {
		t.Fatal("session for /tmp/x not found in tree")
	}
	if found.sessionTokens != 42 {
		t.Errorf("SessionTokens = %d, want 42", found.sessionTokens)
	}
	if found.contextTokens != 100 {
		t.Errorf("ContextTokens = %d, want 100", found.contextTokens)
	}
	if found.model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want claude-sonnet-4-6", found.model)
	}
	// Directory token total must equal session total (only one session in /tmp/x)
	for _, d := range tree.Dirs {
		if d.Path == "/tmp/x" && d.TotalTokens != 42 {
			t.Errorf("Directory /tmp/x TotalTokens = %d, want 42", d.TotalTokens)
		}
	}
}

func TestSnapshotPopulatesTerminalHostCache(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../../tests/fixtures/sessions",
		ClaudeHome:  "../../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.Pricer = &fakeCostPricer{}

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.terminalHostCache) == 0 {
		t.Error("terminalHostCache should be populated after Snapshot")
	}
}

func TestSnapshotPopulatesTranscriptCache(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../../tests/fixtures/sessions",
		ClaudeHome:  "../../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.Pricer = &fakeCostPricer{}

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.transcriptCache) == 0 {
		t.Error("transcriptCache should be populated after Snapshot")
	}
}

func TestSnapshotTerminalHostCacheRetainsAcrossPolls(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../../tests/fixtures/sessions",
		ClaudeHome:  "../../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.Pricer = &fakeCostPricer{}

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstCache := maps.Clone(p.terminalHostCache)

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	for pid, host := range firstCache {
		if got := p.terminalHostCache[pid]; got != host {
			t.Errorf("terminalHostCache[%d]: first=%q second=%q (changed unexpectedly)", pid, host, got)
		}
	}
}

func TestSnapshotTranscriptCacheRetainsAcrossPolls(t *testing.T) {
	p := &Poller{
		SessionsDir: "../../../tests/fixtures/sessions",
		ClaudeHome:  "../../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
	}
	p.Pricer = &fakeCostPricer{}

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstCache := make(map[string]string, len(p.transcriptCache))
	for id, entry := range p.transcriptCache {
		firstCache[id] = entry.path
	}

	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, path := range firstCache {
		if got := p.transcriptCache[id].path; got != path {
			t.Errorf("transcriptCache[%s].path: first=%q second=%q (changed unexpectedly)", id, path, got)
		}
	}
}

func TestSnapshotPRLookupCalledOncePerDir(t *testing.T) {
	type call struct{ cwd, branch string }
	var calls []call

	p := &Poller{
		SessionsDir: "../../../tests/fixtures/sessions",
		ClaudeHome:  "../../../tests/fixtures/claude-home",
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Now() },
		Pricer:      &fakeCostPricer{},
		PRLookupFn: func(_ context.Context, cwd, branch string) (*session.PRInfo, error) {
			calls = append(calls, call{cwd, branch})
			return nil, nil
		},
	}
	_, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// PRLookupFn must not be called for sessions with empty branch.
	for _, c := range calls {
		if c.branch == "" {
			t.Errorf("PRLookupFn called with empty branch for cwd=%q", c.cwd)
		}
	}
	// Must be called at most once per cwd.
	cwdCount := map[string]int{}
	for _, c := range calls {
		cwdCount[c.cwd]++
	}
	for cwd, count := range cwdCount {
		if count > 1 {
			t.Errorf("PRLookupFn called %d times for cwd=%q, want at most 1", count, cwd)
		}
	}
}

// makeSubagentDisruptFixture writes a session with no main-transcript error
// plus a subagents/agent-*.jsonl that carries a terminal unknown API error.
// Returns (sessionsDir, claudeHome).
func makeSubagentDisruptFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	claudeHome := filepath.Join(root, "claude-home")
	cwd := filepath.Join(root, "cwd")
	slug := strings.NewReplacer("/", "-", "_", "-").Replace(cwd)
	projectDir := filepath.Join(claudeHome, "projects", slug)
	sessID := "sub-sess"
	subagentsDir := filepath.Join(projectDir, sessID, "subagents")
	for _, d := range []string{sessionsDir, projectDir, cwd, subagentsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionJSON := fmt.Sprintf(`{"pid":99002,"sessionId":%q,"cwd":%q,"startedAt":1776000000000,"kind":"interactive","entrypoint":"cli"}`, sessID, cwd)
	if err := os.WriteFile(filepath.Join(sessionsDir, "99002.json"), []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// Main transcript: no API error.
	mainTranscript := `{"type":"user","message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, sessID+".jsonl"), []byte(mainTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	// Subagent transcript: a terminal unknown API error.
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	subTranscript := fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"error":"unknown","isApiErrorMessage":true,"message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Stream idle timeout - partial response received"}]}}`+"\n",
		ts.UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-aaaa.jsonl"), []byte(subTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	return sessionsDir, claudeHome
}

func TestSnapshotSubagentDisruptSurfacedAsLastError(t *testing.T) {
	sessionsDir, claudeHome := makeSubagentDisruptFixture(t)
	p := &Poller{
		SessionsDir: sessionsDir,
		ClaudeHome:  claudeHome,
		PidAlive:    func(int) bool { return true },
		Now:         func() time.Time { return time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC) },
		Pricer:      &fakeCostPricer{},
	}
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range tree.Dirs {
		for _, s := range d.Sessions {
			if s.SessionID != "sub-sess" {
				continue
			}
			found = true
			le := s.LastError
			if le == nil {
				t.Fatal("LastError = nil, want subagent error surfaced")
			}
			if !le.FromSubagent {
				t.Errorf("LastError.FromSubagent = false, want true")
			}
		}
	}
	if !found {
		t.Fatal("session sub-sess not found in tree")
	}
}

// mustWrite writes body to dir/name, fataling the test on any error.
func mustWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPoller_WritesToStores(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ws := service.NewWriteService(service.WriteDeps{
		Sessions:      sqlite.NewSessionStore(db),
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws.Start(ctx)

	dir := t.TempDir()
	mustWrite(t, dir, "a.json", `{"pid":12345,"sessionId":"sid-a","cwd":"/p"}`)

	p := &Poller{
		SessionsDir:      dir,
		PidAlive:         func(int) bool { return true },
		WorkingThreshold: 30 * time.Second,
		IdleThreshold:    10 * time.Minute,
		Now:              time.Now,
		Signalers:        nil,
		WriteService:     ws,
	}
	if _, _, err := p.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := ws.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	ids, err := sqlite.NewSessionStore(db).AllSessionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "sid-a" {
		t.Errorf("AllSessionIDs = %v, want [sid-a]", ids)
	}
}

// makeRegistryFixture writes a session registry file with the given status /
// waitingFor / statusUpdatedAt plus a main transcript whose last message event
// (and mtime) sit at msgTime. Returns (sessionsDir, claudeHome).
func makeRegistryFixture(t *testing.T, status, waitingFor string, statusUpdatedAt, msgTime time.Time) (string, string) {
	t.Helper()
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	claudeHome := filepath.Join(root, "claude-home")
	cwd := filepath.Join(root, "cwd")
	slug := strings.NewReplacer("/", "-", "_", "-").Replace(cwd)
	projectDir := filepath.Join(claudeHome, "projects", slug)
	for _, d := range []string{sessionsDir, projectDir, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionJSON := fmt.Sprintf(
		`{"pid":99050,"sessionId":"reg-sess","cwd":%q,"startedAt":1776000000000,"kind":"interactive","entrypoint":"cli","status":%q,"waitingFor":%q,"statusUpdatedAt":%d}`,
		cwd, status, waitingFor, statusUpdatedAt.UnixMilli(),
	)
	if err := os.WriteFile(filepath.Join(sessionsDir, "99050.json"), []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	tx := `{"type":"assistant","timestamp":"` + msgTime.UTC().Format(time.RFC3339Nano) +
		`","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	txPath := filepath.Join(projectDir, "reg-sess.jsonl")
	if err := os.WriteFile(txPath, []byte(tx), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(txPath, msgTime, msgTime); err != nil {
		t.Fatal(err)
	}
	return sessionsDir, claudeHome
}

func snapshotStatus(t *testing.T, p *Poller) session.Status {
	t.Helper()
	return snapshotSession(t, p).Status
}

// snapshotSession returns the first SessionView in the snapshot tree, exposing
// Status/Blocker/LongIdle for the ADR 0024 derivation tests.
func snapshotSession(t *testing.T, p *Poller) *aggregate.SessionView {
	t.Helper()
	tree, _, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range tree.Dirs {
		for _, s := range d.Sessions {
			return s
		}
	}
	t.Fatal("no session in tree")
	return nil
}

// makeErrorFixture writes a busy-registry session whose main transcript ends
// with an isApiErrorMessage assistant event of the given kind/text. When
// superseded is true a trailing user message is appended so the tail-walk marks
// the error NON-terminal (newer activity). Returns (sessionsDir, claudeHome).
func makeErrorFixture(t *testing.T, errKind, errText string, superseded bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	claudeHome := filepath.Join(root, "claude-home")
	cwd := filepath.Join(root, "cwd")
	slug := strings.NewReplacer("/", "-", "_", "-").Replace(cwd)
	projectDir := filepath.Join(claudeHome, "projects", slug)
	for _, d := range []string{sessionsDir, projectDir, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionJSON := fmt.Sprintf(
		`{"pid":99060,"sessionId":"err-sess","cwd":%q,"startedAt":1776000000000,"kind":"interactive","entrypoint":"cli","status":"busy"}`,
		cwd,
	)
	if err := os.WriteFile(filepath.Join(sessionsDir, "99060.json"), []byte(sessionJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	errTS := time.Date(2026, 6, 18, 11, 59, 0, 0, time.UTC).UTC().Format(time.RFC3339Nano)
	body := fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"error":%q,"isApiErrorMessage":true,"message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n",
		errTS, errKind, errText,
	)
	if superseded {
		body += `{"type":"user","message":{"role":"user","content":"continue"}}` + "\n"
	}
	if err := os.WriteFile(filepath.Join(projectDir, "err-sess.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return sessionsDir, claudeHome
}

func newErrorPoller(sessionsDir, claudeHome string, now time.Time) *Poller {
	return &Poller{
		SessionsDir: sessionsDir, ClaudeHome: claudeHome,
		PidAlive:         func(int) bool { return true },
		Now:              func() time.Time { return now },
		WorkingThreshold: 30 * time.Second, IdleThreshold: 10 * time.Minute,
	}
}

func TestSnapshotBlocker_AuthFailureIsHumanAuthn(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	sd, ch := makeErrorFixture(t, "authentication_failed", "HTTP 401", false)
	sv := snapshotSession(t, newErrorPoller(sd, ch, now))
	if sv.Status != session.Blocked || sv.Blocker != session.HumanAuthn {
		t.Errorf("401 → status=%v blocker=%v, want Blocked/human_authn", sv.Status, sv.Blocker)
	}
}

func TestSnapshotBlocker_TerminalRateLimitIsUsageLimit(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// Text has no parseable reset, so this exercises the LastError-derived
	// usage_limit path (NOT RateLimitResetsAt). The registry is busy — the
	// terminal rate-limit MUST override busy → blocked/usage_limit (the live
	// findev-deep-dive mis-report this ADR corrects).
	sd, ch := makeErrorFixture(t, "rate_limit", "You've hit your org's monthly spend limit", false)
	sv := snapshotSession(t, newErrorPoller(sd, ch, now))
	if sv.Status != session.Blocked || sv.Blocker != session.UsageLimit {
		t.Errorf("terminal 429 → status=%v blocker=%v, want Blocked/usage_limit", sv.Status, sv.Blocker)
	}
}

func TestSnapshotBlocker_GenericTerminalErrorIsError(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	sd, ch := makeErrorFixture(t, "server_error", "API Error 500", false)
	sv := snapshotSession(t, newErrorPoller(sd, ch, now))
	if sv.Status != session.Blocked || sv.Blocker != session.ErrorBlocker {
		t.Errorf("generic terminal error → status=%v blocker=%v, want Blocked/error", sv.Status, sv.Blocker)
	}
}

func TestSnapshotBlocker_SupersededErrorIsWorking(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// Same terminal-shaped error but a newer user message supersedes it, so the
	// error is NOT current (IsTerminal=false). busy is trusted → Working.
	sd, ch := makeErrorFixture(t, "rate_limit", "You've hit your limit", true)
	sv := snapshotSession(t, newErrorPoller(sd, ch, now))
	if sv.Status != session.Working || sv.Blocker != session.NoBlocker {
		t.Errorf("superseded error → status=%v blocker=%v, want Working/none", sv.Status, sv.Blocker)
	}
}

// TestDeriveStatusBlocker_SubagentErrorDoesNotBlock guards the fix for the
// stale-subagent-error blocker: a FromSubagent terminal error is display-only
// and MUST NOT override the parent's activity verdict. A subagent's one-shot
// agent-*.jsonl file never receives superseding activity, so its IsTerminal
// stays true for the life of the parent — treating it as blocking would pin an
// alive, working parent to Blocked forever (and hold the Mac awake for a
// retryable disrupt that is never nudged, since the nudge path excludes
// FromSubagent).
func TestDeriveStatusBlocker_SubagentErrorDoesNotBlock(t *testing.T) {
	subErr := &transcript.ErrorRecord{IsTerminal: true, FromSubagent: true, Kind: transcript.ErrRateLimit}
	if st, bl := deriveStatusBlocker(claudetranscript.Active, subErr, time.Time{}); st != session.Working || bl != session.NoBlocker {
		t.Errorf("subagent terminal error + active → %v/%v, want Working/none", st, bl)
	}
	// Sanity: the SAME error kind on the MAIN session (not FromSubagent) DOES
	// block — the exclusion is scoped to subagent-surfaced errors only.
	mainErr := &transcript.ErrorRecord{IsTerminal: true, FromSubagent: false, Kind: transcript.ErrRateLimit}
	if st, bl := deriveStatusBlocker(claudetranscript.Active, mainErr, time.Time{}); st != session.Blocked || bl != session.UsageLimit {
		t.Errorf("main terminal 429 + active → %v/%v, want Blocked/usage_limit", st, bl)
	}
}

func TestSnapshotVerdict_BusyIsWorking(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// busy status, but the main transcript is 20 min stale (subagent run).
	// busy is TRUSTED → Working, never demoted.
	sessionsDir, claudeHome := makeRegistryFixture(t, "busy", "", now.Add(-20*time.Minute), now.Add(-20*time.Minute))
	p := &Poller{
		SessionsDir: sessionsDir, ClaudeHome: claudeHome,
		PidAlive:         func(int) bool { return true },
		Now:              func() time.Time { return now },
		WorkingThreshold: 30 * time.Second, IdleThreshold: 10 * time.Minute,
	}
	if got := snapshotStatus(t, p); got != session.Working {
		t.Errorf("busy stale-transcript Status = %v, want Working (trusted)", got)
	}
}

func TestSnapshotVerdict_FreshWaitingIsBlockedHumanInput(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// waiting + transcript fresh relative to statusUpdatedAt → blocked/human_input.
	sessionsDir, claudeHome := makeRegistryFixture(t, "waiting", "permission prompt", now.Add(-30*time.Second), now.Add(-30*time.Second))
	p := &Poller{
		SessionsDir: sessionsDir, ClaudeHome: claudeHome,
		PidAlive:         func(int) bool { return true },
		Now:              func() time.Time { return now },
		WorkingThreshold: 30 * time.Second, IdleThreshold: 10 * time.Minute,
		WaitingFreshWindow: 60 * time.Second,
	}
	sv := snapshotSession(t, p)
	if sv.Status != session.Blocked || sv.Blocker != session.HumanInput {
		t.Errorf("fresh waiting → status=%v blocker=%v, want Blocked/human_input", sv.Status, sv.Blocker)
	}
}

func TestSnapshotVerdict_StaleWaitingFallsThrough(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// waiting set 16h ago but the transcript advanced to 15m ago → the stale
	// "waiting" flag falls through (NOT WaitingForHuman). With an alive pid the
	// existing Dormant→Idle clamp keeps the freshest-transcript session Idle.
	sessionsDir, claudeHome := makeRegistryFixture(t, "waiting", "permission prompt", now.Add(-16*time.Hour), now.Add(-15*time.Minute))
	p := &Poller{
		SessionsDir: sessionsDir, ClaudeHome: claudeHome,
		PidAlive:           func(int) bool { return true },
		Now:                func() time.Time { return now },
		WorkingThreshold:   30 * time.Second,
		IdleThreshold:      10 * time.Minute,
		WaitingFreshWindow: 60 * time.Second,
	}
	got := snapshotStatus(t, p)
	if got == session.Blocked {
		t.Errorf("stale waiting Status = %v, want non-blocked (fell through)", got)
	}
	if got != session.Idle {
		t.Errorf("stale waiting + alive pid Status = %v, want Idle (clamp bump)", got)
	}
}

func TestSnapshotVerdict_DeadIdleStaleIsLongIdle(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// idle + 15m-old transcript + dead pid → Idle with the LongIdle age
	// refinement (ADR 0024: Dormant is no longer a status; the alive-pid clamp
	// does not apply to dead pids).
	sessionsDir, claudeHome := makeRegistryFixture(t, "idle", "", now.Add(-15*time.Minute), now.Add(-15*time.Minute))
	p := &Poller{
		SessionsDir: sessionsDir, ClaudeHome: claudeHome,
		PidAlive:         func(int) bool { return false },
		Now:              func() time.Time { return now },
		WorkingThreshold: 30 * time.Second,
		IdleThreshold:    10 * time.Minute,
	}
	sv := snapshotSession(t, p)
	if sv.Status != session.Idle || !sv.LongIdle {
		t.Errorf("dead idle 15m-old → status=%v longIdle=%v, want Idle/true", sv.Status, sv.LongIdle)
	}
}

func TestSnapshotVerdict_IdleStatusFreshIsIdle(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	sessionsDir, claudeHome := makeRegistryFixture(t, "idle", "", now.Add(-5*time.Second), now.Add(-5*time.Second))
	p := &Poller{
		SessionsDir: sessionsDir, ClaudeHome: claudeHome,
		PidAlive:         func(int) bool { return true },
		Now:              func() time.Time { return now },
		WorkingThreshold: 30 * time.Second, IdleThreshold: 10 * time.Minute,
	}
	if got := snapshotStatus(t, p); got != session.Idle {
		t.Errorf("fresh idle Status = %v, want Idle", got)
	}
}

func TestSnapshotVerdict_DeadPidNeverWorking(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// busy + fresh transcript, but pid is dead → must not be Working.
	sessionsDir, claudeHome := makeRegistryFixture(t, "busy", "", now.Add(-5*time.Second), now.Add(-5*time.Second))
	p := &Poller{
		SessionsDir: sessionsDir, ClaudeHome: claudeHome,
		PidAlive:         func(int) bool { return false },
		Now:              func() time.Time { return now },
		WorkingThreshold: 30 * time.Second, IdleThreshold: 10 * time.Minute,
	}
	if got := snapshotStatus(t, p); got == session.Working {
		t.Errorf("dead pid Status = Working, want not-Working")
	}
}
