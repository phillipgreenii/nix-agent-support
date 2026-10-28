package internal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	line200 = `pjira-ratelimit: {"status":200,"limit":100,"remaining":97,"near_limit":false}`
	line429 = `pjira-ratelimit: {"status":429,"limit":100,"remaining":0,"near_limit":true,"retry_after_seconds":11,"reset":"2026-10-27T12:00:11Z","reason":"jira-burst-based"}`
)

func TestParseRateLimit(t *testing.T) {
	t.Run("a 200 line carries limit, remaining and near_limit", func(t *testing.T) {
		rl, ok := ParseRateLimit([]byte(line200 + "\n"))
		if !ok || rl.Status != 200 || rl.Limit == nil || *rl.Limit != 100 || rl.Remaining == nil || *rl.Remaining != 97 ||
			rl.NearLimit == nil || *rl.NearLimit || rl.RetryAfterSeconds != 0 || rl.Reset != "" || rl.Reason != "" {
			t.Fatalf("rl = %+v ok=%v", rl, ok)
		}
	})
	t.Run("a 429 line carries retry-after, reset and reason", func(t *testing.T) {
		rl, ok := ParseRateLimit([]byte(line429))
		if !ok || rl.Status != 429 || *rl.Remaining != 0 || !*rl.NearLimit || rl.RetryAfterSeconds != 11 ||
			rl.Reset != "2026-10-27T12:00:11Z" || rl.Reason != "jira-burst-based" {
			t.Fatalf("rl = %+v ok=%v", rl, ok)
		}
	})
	t.Run("a remaining of 0 is a value, an absent remaining is nil", func(t *testing.T) {
		z, _ := ParseRateLimit([]byte(`pjira-ratelimit: {"status":429,"remaining":0}`))
		if z.Remaining == nil || *z.Remaining != 0 {
			t.Errorf("remaining 0 must be stated as 0: %+v", z)
		}
		a, ok := ParseRateLimit([]byte(`pjira-ratelimit: {"status":503,"retry_after_seconds":5}`))
		if !ok || a.Remaining != nil || a.Limit != nil || a.NearLimit != nil || a.RetryAfterSeconds != 5 {
			t.Errorf("absent keys must stay absent: %+v", a)
		}
	})
	t.Run("the last line is the freshest and other stderr lines are ignored", func(t *testing.T) {
		in := "warning: something\n" + line200 + "\nnoise\n" + line429 + "\ntrailing text without a newline"
		rl, ok := ParseRateLimit([]byte(in))
		if !ok || rl.Status != 429 {
			t.Fatalf("want the 429 line, got %+v ok=%v", rl, ok)
		}
	})
	t.Run("a malformed or status-less line is skipped, an earlier good line still counts", func(t *testing.T) {
		in := line200 + "\npjira-ratelimit: {not json\npjira-ratelimit: {\"limit\":5}\n"
		rl, ok := ParseRateLimit([]byte(in))
		if !ok || rl.Status != 200 {
			t.Fatalf("want the 200 line, got %+v ok=%v", rl, ok)
		}
	})
	t.Run("a CRLF line parses", func(t *testing.T) {
		if rl, ok := ParseRateLimit([]byte(line200 + "\r\n")); !ok || rl.Status != 200 {
			t.Fatalf("rl = %+v ok=%v", rl, ok)
		}
	})
	t.Run("no line, a prefix-lookalike and empty input are no reading", func(t *testing.T) {
		for _, in := range []string{"", "boom\n", " " + line200, "pjira-ratelimit:" + `{"status":200}`} {
			if rl, ok := ParseRateLimit([]byte(in)); ok {
				t.Errorf("%q: want no reading, got %+v", in, rl)
			}
		}
	})
}

func TestStripRateLimitLines(t *testing.T) {
	in := "first\n" + line200 + "\nerror: boom\n" + line429 + "\n"
	if got := string(StripRateLimitLines([]byte(in))); got != "first\nerror: boom\n" {
		t.Errorf("got %q", got)
	}
	plain := []byte("no lines here\n")
	if got := StripRateLimitLines(plain); string(got) != string(plain) {
		t.Errorf("text without lines must come back unchanged, got %q", got)
	}
}

func stubWithStderr(t *testing.T, stderr string, exit int, stdout string) string {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s' '" + stdout + "'\ncat <<'PJIRASTUBEOF' >&2\n" + stderr + "\nPJIRASTUBEOF\nexit " + string(rune('0'+exit)) + "\n"
	path := filepath.Join(t.TempDir(), "pjira")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// A real exec of a stub that writes the lines: the sink gets the freshest
// reading on success, and on a 429 failure the reading is delivered while the
// error text carries the real message without the lines.
func TestCLIRunner_DeliversRateLimitReading(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		r := &CLIRunner{BinaryOverride: stubWithStderr(t, line200, 0, "{}")}
		var got []RateLimit
		out, err := r.Run(WithRateLimitSink(context.Background(), func(rl RateLimit) { got = append(got, rl) }), "issue", "--", "PROJ-1")
		if err != nil || out != "{}" {
			t.Fatalf("out=%q err=%v", out, err)
		}
		if len(got) != 1 || got[0].Status != 200 || *got[0].Remaining != 97 {
			t.Fatalf("sink got %+v", got)
		}
	})
	t.Run("429 failure", func(t *testing.T) {
		r := &CLIRunner{BinaryOverride: stubWithStderr(t, line429+"\npjira: search: status 429 Too Many Requests", 1, "")}
		var got *RateLimit
		_, err := r.Run(WithRateLimitSink(context.Background(), func(rl RateLimit) { got = &rl }), "search")
		if err == nil {
			t.Fatal("want an error")
		}
		if got == nil || got.Status != 429 || got.RetryAfterSeconds != 11 {
			t.Fatalf("sink got %+v", got)
		}
		if strings.Contains(err.Error(), RateLimitPrefix) || !strings.Contains(err.Error(), "status 429") {
			t.Errorf("the error must carry the message and not the reading line: %v", err)
		}
	})
	t.Run("no line and no sink are both fine", func(t *testing.T) {
		r := &CLIRunner{BinaryOverride: stubWithStderr(t, "plain stderr", 0, "{}")}
		called := false
		if _, err := r.Run(WithRateLimitSink(context.Background(), func(RateLimit) { called = true }), "issue"); err != nil || called {
			t.Fatalf("err=%v called=%v (a pjira without the lines is no reading, not an error)", err, called)
		}
		r2 := &CLIRunner{BinaryOverride: stubWithStderr(t, line200, 0, "{}")}
		if _, err := r2.Run(context.Background(), "issue"); err != nil {
			t.Fatalf("a run with no sink in the context: %v", err)
		}
	})
}
