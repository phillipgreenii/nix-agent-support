package telemetry

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestFanout_HandleReachesAllChildren(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	h := Fanout(slog.NewJSONHandler(&buf1, nil), slog.NewJSONHandler(&buf2, nil))
	slog.New(h).Info("hello", "k", "v")
	if !strings.Contains(buf1.String(), "hello") {
		t.Errorf("child 1 missing record: %q", buf1.String())
	}
	if !strings.Contains(buf2.String(), "hello") {
		t.Errorf("child 2 missing record: %q", buf2.String())
	}
}

func TestFanout_WithAttrsPropagatesToAllChildren(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	log := slog.New(Fanout(
		slog.NewJSONHandler(&buf1, nil),
		slog.NewJSONHandler(&buf2, nil),
	)).With("svc", "pg-pr")
	log.Info("msg")
	for i, b := range []*bytes.Buffer{&buf1, &buf2} {
		if !strings.Contains(b.String(), `"svc":"pg-pr"`) {
			t.Errorf("child %d missing attr: %q", i+1, b.String())
		}
	}
}

func TestFanout_EnabledIfAnyChildEnabled(t *testing.T) {
	errorOnly := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})
	infoOK := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})
	if !Fanout(errorOnly, infoOK).Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected enabled at Info when one child is")
	}
	if Fanout(errorOnly).Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected disabled at Info when no child is")
	}
}

func TestFanout_PerChildLevelFiltering(t *testing.T) {
	var errBuf, infoBuf bytes.Buffer
	errorOnly := slog.NewJSONHandler(&errBuf, &slog.HandlerOptions{Level: slog.LevelError})
	infoOK := slog.NewJSONHandler(&infoBuf, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.New(Fanout(errorOnly, infoOK)).Info("only-info")
	if errBuf.Len() != 0 {
		t.Errorf("error-only child should have filtered Info: %q", errBuf.String())
	}
	if !strings.Contains(infoBuf.String(), "only-info") {
		t.Errorf("info child missing record: %q", infoBuf.String())
	}
}
