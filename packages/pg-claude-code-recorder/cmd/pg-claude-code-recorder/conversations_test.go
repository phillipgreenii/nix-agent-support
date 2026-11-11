package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

const cbase = int64(1793375000000)

func run(t *testing.T, e *clienttest.Env, args ...string) (int, string, string) {
	t.Helper()
	a, out, errw := testApp(nil)
	code := a.run(append(args, "--socket", e.Socket))
	return code, out.String(), errw.String()
}

// fakeList serves a canned conversation.list answer.
func fakeList(t *testing.T, ans lifecycle.ConversationList) *clienttest.Env {
	t.Helper()
	sock := shortDir(t) + "/r.sock"
	s := server.New(obs.New(nil), nil, func() any { return map[string]any{} })
	s.Register("conversation.list", server.KindQuery, func(context.Context, []byte) (any, *wire.Error) { return ans, nil })
	if err := s.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return &clienttest.Env{Socket: sock}
}

func TestWhileTheRecorderIsBehindTheCrashQueryExits5WithThePartialList(t *testing.T) {
	end := time.UnixMilli(cbase).UTC()
	row := lifecycle.Conversation{
		SessionID: "example-partial", State: "closed", Stage: "quiet", EndTime: &end,
		Evidence: lifecycle.Evidence{Ended: true, ExitObserved: true, RegistryPresentAtExit: true, Cwd: "/example/work"},
	}
	e := fakeList(t, lifecycle.ConversationList{Conversations: []lifecycle.Conversation{row}, Degraded: true})
	code, out, errs := run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z", "--json")
	if code != 5 || !strings.Contains(out, "example-partial") || !strings.Contains(errs, "behind") {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	if code, _, _ = run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z", "--allow-partial"); code != 0 {
		t.Fatalf("--allow-partial: exit %d", code)
	}
	// An empty crashed result exits 5 too while behind.
	e = fakeList(t, lifecycle.ConversationList{Degraded: true})
	if code, out, _ = run(t, e, "conversations", "--crashed", "--json"); code != 5 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty while behind: %d %q", code, out)
	}
}

func TestAnUnreadableCwdStillGivesAResumeLine(t *testing.T) {
	end := time.UnixMilli(cbase).UTC()
	row := lifecycle.Conversation{
		SessionID: "example-nocwd", State: "closed", Stage: "quiet", EndTime: &end,
		Evidence: lifecycle.Evidence{Ended: true, ExitObserved: true, RegistryPresentAtExit: true},
	}
	e := fakeList(t, lifecycle.ConversationList{Conversations: []lifecycle.Conversation{row}})
	code, out, _ := run(t, e, "conversations", "--crashed", "--since", "2000-01-01T00:00:00Z")
	if code != 0 || !strings.Contains(out, "(cwd unknown)") || !strings.Contains(out, "claude --resume example-nocwd") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestParseSinceTakesADurationOrATime(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if got, err := parseSince("", now); err != nil || !got.Equal(now.Add(-7*24*time.Hour)) {
		t.Errorf("default %v %v", got, err)
	}
	if got, err := parseSince("36h", now); err != nil || !got.Equal(now.Add(-36*time.Hour)) {
		t.Errorf("36h %v %v", got, err)
	}
	if got, err := parseSince("2d", now); err != nil || !got.Equal(now.Add(-48*time.Hour)) {
		t.Errorf("2d %v %v", got, err)
	}
	if got, err := parseSince("2026-10-01T00:00:00Z", now); err != nil || got.Day() != 1 {
		t.Errorf("RFC 3339 %v %v", got, err)
	}
	if _, err := parseSince("soon", now); err == nil {
		t.Error("soon parsed")
	}
}
