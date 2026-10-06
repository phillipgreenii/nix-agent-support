package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// logRecord renders one record in logFormat's layout.
func logRecord(sha, authorDate, email, subject string) string {
	return recordSep + strings.Join([]string{sha, authorDate, email, "", subject}, fieldSep)
}

func TestParseLog(t *testing.T) {
	out := logRecord("aaa", "2026-09-05T10:00:00-07:00", "me@example.test", "first subject") + "\n" +
		logRecord("bbb", "2026-09-06T10:00:00Z", "other@example.test", "subject: with | odd chars")
	recs, err := parseLog(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].sha != "aaa" || recs[0].subject != "first subject" || recs[1].subject != "subject: with | odd chars" {
		t.Errorf("recs = %+v", recs)
	}
	if recs[0].when.UTC() != time.Date(2026, 9, 5, 17, 0, 0, 0, time.UTC) {
		t.Errorf("when = %v", recs[0].when)
	}
	if recs, err := parseLog(""); err != nil || len(recs) != 0 {
		t.Errorf("empty log: recs=%v err=%v", recs, err)
	}
	if _, err := parseLog(recordSep + "only-one-field"); err == nil {
		t.Error("want an error for a malformed record")
	}
	if _, err := parseLog(logRecord("ccc", "not-a-date", "x@example.test", "s")); err == nil {
		t.Error("want an error for an unparseable author date")
	}
}

// TestCollect_ExactEmailAndRangeDecidedHere drives the collector with a fake
// runner whose git "returns" a superset: a substring-matching author, and
// commits outside the range. The collector alone decides.
func TestCollect_ExactEmailAndRangeDecidedHere(t *testing.T) {
	dir := t.TempDir()
	log := strings.Join([]string{
		logRecord("s1", "2026-09-05T10:00:00Z", "me@example.test", "keep"),
		logRecord("s2", "2026-09-05T10:00:00Z", "notme@example.test", "substring author"),
		logRecord("s3", "2026-09-01T00:00:00Z", "ME@example.test", "since is inclusive, email case-insensitive"),
		logRecord("s4", "2026-10-01T00:00:00Z", "me@example.test", "before is exclusive"),
		logRecord("s5", "2026-08-31T23:59:59Z", "me@example.test", "too early"),
		logRecord("s1", "2026-09-05T10:00:00Z", "me@example.test", "duplicate sha read once"),
	}, "\n")
	var logArgs []string
	r := &fakeRunner{run: func(_ string, args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return ".git", nil
		case "log":
			logArgs = args
			return log, nil
		case "for-each-ref":
			return "refs/heads/main", nil
		case "diff-tree":
			return "", nil
		}
		return "", errors.New("exit status 1") // no origin remote
	}}
	b := New(Options{Stderr: &bytes.Buffer{}, Runner: r})
	b.now = func() time.Time { return pinnedNow }

	items, truncated, err := b.collectCommits(context.Background(),
		Config{RepoPaths: []string{dir}}, []string{"me@example.test"}, rangeSince, rangeBefore)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if got := subjects(items); strings.Join(got, "|") != "keep|since is inclusive, email case-insensitive" {
		t.Errorf("subjects = %v, want keep and the since-boundary commit", got)
	}

	joined := strings.Join(logArgs, " ")
	for _, bad := range []string{"--since", "--until", "--all", "--after", "--before"} {
		if strings.Contains(joined, bad) {
			t.Errorf("log args %q must not contain %s (git filters those on committer date)", joined, bad)
		}
	}
	for _, want := range []string{"--branches", "--no-merges", "--author=me@example.test"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log args %q missing %s", joined, want)
		}
	}
}

func TestCollect_IncludeMergesOmitsNoMerges(t *testing.T) {
	dir := t.TempDir()
	var logArgs []string
	r := &fakeRunner{run: func(_ string, args ...string) (string, error) {
		if args[0] == "log" {
			logArgs = args
		}
		return "", nil
	}}
	b := New(Options{Stderr: &bytes.Buffer{}, Runner: r})
	if _, _, err := b.collectCommits(context.Background(),
		Config{RepoPaths: []string{dir}, IncludeMerges: true}, []string{"me@example.test"}, time.Time{}, rangeBefore); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(logArgs, " "), "--no-merges") {
		t.Errorf("log args %v must omit --no-merges when include_merges is true", logArgs)
	}
}

func TestCollect_GitFailureOnValidRepoIsNotSilent(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{run: func(_ string, args ...string) (string, error) {
		if args[0] == "log" {
			return "", errors.New("git log: exit status 128: bad object")
		}
		return ".git", nil
	}}
	var stderr bytes.Buffer
	b := New(Options{Stderr: &stderr, Runner: r})
	_, _, err := b.collectCommits(context.Background(), Config{RepoPaths: []string{dir}}, []string{"me@example.test"}, time.Time{}, rangeBefore)
	if err == nil || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "bad object") {
		t.Errorf("err = %v, want it to name the repo and the git failure", err)
	}
}

func TestLogSkipWritesExactlyOneLine(t *testing.T) {
	var stderr bytes.Buffer
	b := New(Options{Stderr: &stderr, Runner: &fakeRunner{}})
	b.logSkip("/some/path", "not a git repository")
	got := stderr.String()
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") || !strings.Contains(got, "/some/path") || !strings.Contains(got, "not a git repository") {
		t.Errorf("logSkip wrote %q", got)
	}
	// A path with a newline must still be one line.
	stderr.Reset()
	b.logSkip("/odd\npath", "x")
	if strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("logSkip with a newline in the path wrote %q", stderr.String())
	}
}

func TestNew_DefaultsRunnerAndStderr(t *testing.T) {
	b := New(Options{})
	if b.runner == nil || b.stderr != os.Stderr || b.now == nil {
		t.Errorf("defaults not applied: %+v", b)
	}
}

func TestDedupeByID_FirstWinsAndKeepsOrder(t *testing.T) {
	mk := func(id, path string) schema.ActivityItem {
		return schema.ActivityItem{ID: id, Fields: json.RawMessage(`{"repo_path":"` + path + `"}`)}
	}
	got := dedupeByID([]schema.ActivityItem{mk("a", "/1"), mk("b", "/1"), mk("a", "/2"), mk("c", "/2"), mk("b", "/3")})
	var ids, paths []string
	for _, it := range got {
		ids = append(ids, it.ID)
		paths = append(paths, string(it.Fields))
	}
	if strings.Join(ids, ",") != "a,b,c" || !strings.Contains(paths[0], "/1") || !strings.Contains(paths[2], "/2") {
		t.Errorf("ids=%v fields=%v", ids, paths)
	}
	if empty := dedupeByID(nil); empty == nil || len(empty) != 0 {
		t.Errorf("dedupeByID(nil) = %#v, want a non-nil empty slice", empty)
	}
}
