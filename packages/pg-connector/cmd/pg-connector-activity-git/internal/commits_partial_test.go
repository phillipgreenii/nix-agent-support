package internal

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// cancelOnBatch cancels the op context the moment the batched line-count read
// of repo `at` starts, then lets the real git call run (and fail on the dead
// context), standing in for the op deadline firing mid-enrichment.
func cancelOnBatch(cancel context.CancelFunc, at string) *countingRunner {
	r := newCountingRunner()
	r.hook = func(args []string) error {
		if len(args) > 1 && args[0] == "log" && args[1] == "--no-walk=unsorted" && strings.Contains(strings.Join(args, " "), at) {
			cancel()
		}
		return nil
	}
	return r
}

// twoRepos builds two repos with one in-range commit each. Because the batch
// call names its shas, the hook keys on the second repo's sha.
func twoRepos(t *testing.T) (repoA, repoB, shaB string) {
	t.Helper()
	f := newGitFixture(t)
	const day = "2026-09-05T10:00:00+00:00"
	repoA, repoB = f.newRepo(), f.newRepo()
	f.commit(repoA, meEmail, day, day, "in-a")
	shaB = f.commit(repoB, meEmail, day, day, "in-b")
	return repoA, repoB, shaB
}

func TestCollect_DeadlineMidwayKeepsFinishedReposAsTruncated(t *testing.T) {
	repoA, repoB, shaB := twoRepos(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	b := New(Options{Stderr: &stderr, Runner: cancelOnBatch(cancel, shaB)})
	b.now = func() time.Time { return pinnedNow }

	items, truncated, err := b.collectCommits(ctx, Config{RepoPaths: []string{repoA, repoB}},
		[]string{meEmail}, time.Time{}, rangeBefore)
	if err != nil {
		t.Fatalf("a deadline after one repo was read must not discard it: %v", err)
	}
	if !truncated {
		t.Error("truncated = false, want true: the range is incompletely covered")
	}
	if got := subjects(items); strings.Join(got, "|") != "in-a" {
		t.Errorf("subjects = %v, want only the finished repo's commit", got)
	}
	if !strings.Contains(stderr.String(), repoB) || strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("stderr = %q, want one line naming the unfinished repo", stderr.String())
	}
}

func TestCollect_DeadlineBeforeAnyRepoFinishedIsStillAnError(t *testing.T) {
	_, repoB, shaB := twoRepos(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(Options{Stderr: &bytes.Buffer{}, Runner: cancelOnBatch(cancel, shaB)})
	b.now = func() time.Time { return pinnedNow }

	items, truncated, err := b.collectCommits(ctx, Config{RepoPaths: []string{repoB}},
		[]string{meEmail}, time.Time{}, rangeBefore)
	if err == nil || truncated || items != nil {
		t.Errorf("items=%v truncated=%v err=%v, want the error and nothing else", items, truncated, err)
	}
}

func TestCollect_NonDeadlineFailureStillFailsTheWholeRead(t *testing.T) {
	repoA, repoB, _ := twoRepos(t)
	r := newCountingRunner()
	// Every branch read fails (batch and per-commit fallback alike) while the
	// op context stays live.
	r.hook = func(args []string) error {
		if args[0] == "for-each-ref" {
			return errors.New("simulated git failure")
		}
		return nil
	}
	b := New(Options{Stderr: &bytes.Buffer{}, Runner: r})
	b.now = func() time.Time { return pinnedNow }
	items, truncated, err := b.collectCommits(context.Background(), Config{RepoPaths: []string{repoA, repoB}},
		[]string{meEmail}, time.Time{}, rangeBefore)
	if err == nil || truncated || items != nil {
		t.Errorf("items=%v truncated=%v err=%v, want a plain error with a live context", items, truncated, err)
	}
}
