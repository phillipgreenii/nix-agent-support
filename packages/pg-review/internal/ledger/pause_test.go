package ledger

import "testing"

// fileRun makes a run of lens that filed one topic per subject (an occurrence
// each) and returns the topics.
func fileRun(t *testing.T, l *Ledger, lens string, subjects ...string) []Topic {
	t.Helper()
	run := newRun(t, l, lens, "sc")
	var out []Topic
	for _, s := range subjects {
		tp := newTopic(t, l, lens, s, false)
		if err := l.WithTx(ctx, func(tx *Tx) error { return tx.AddOccurrence(ctx, tp.ID, run.ID, "sc", lens, "", "") }); err != nil {
			t.Fatal(err)
		}
		out = append(out, tp)
	}
	return out
}

func reject(t *testing.T, l *Ledger, tps ...Topic) {
	t.Helper()
	for _, tp := range tps {
		if _, err := l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: RulingRejected}); err != nil {
			t.Fatal(err)
		}
	}
}

func paused(t *testing.T, l *Ledger, lens string) bool {
	t.Helper()
	p, err := l.IsLensPaused(ctx, lens)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPausedOnlyAfterThreeAllRejectedRuns(t *testing.T) {
	l := open(t)
	reject(t, l, fileRun(t, l, "lens", "a1", "a2")...)
	reject(t, l, fileRun(t, l, "lens", "b1")...)
	if paused(t, l, "lens") {
		t.Fatal("two runs must not pause")
	}
	reject(t, l, fileRun(t, l, "lens", "c1")...)
	if !paused(t, l, "lens") {
		t.Fatal("three all-Rejected runs must pause")
	}
	if paused(t, l, "other") {
		t.Error("another lens must not be paused")
	}
}

func TestOnePendingItemBlocksPause(t *testing.T) {
	l := open(t)
	reject(t, l, fileRun(t, l, "lens", "a")...)
	b := fileRun(t, l, "lens", "b1", "b2")
	reject(t, l, b[0]) // b2 stays Pending
	reject(t, l, fileRun(t, l, "lens", "c")...)
	if paused(t, l, "lens") {
		t.Fatal("a run with a non-Rejected item must not count")
	}
	reject(t, l, b[1])
	if !paused(t, l, "lens") {
		t.Fatal("pause once every item is Rejected")
	}
}

func TestRunThatFiledNothingIsIgnoredAndDoesNotReset(t *testing.T) {
	l := open(t)
	reject(t, l, fileRun(t, l, "lens", "a")...)
	newRun(t, l, "lens", "sc") // filed nothing
	reject(t, l, fileRun(t, l, "lens", "b")...)
	newRun(t, l, "lens", "sc")
	if paused(t, l, "lens") {
		t.Fatal("only two qualifying runs")
	}
	reject(t, l, fileRun(t, l, "lens", "c")...)
	newRun(t, l, "lens", "sc") // trailing empty run does not reset
	if !paused(t, l, "lens") {
		t.Fatal("empty runs must be ignored, not reset the count")
	}
}

func TestOnlyLastThreeRunsCount(t *testing.T) {
	l := open(t)
	a := fileRun(t, l, "lens", "a") // stays Pending
	reject(t, l, fileRun(t, l, "lens", "b")...)
	reject(t, l, fileRun(t, l, "lens", "c")...)
	if paused(t, l, "lens") {
		t.Fatal("older pending run is among the last three")
	}
	reject(t, l, fileRun(t, l, "lens", "d")...)
	if !paused(t, l, "lens") {
		t.Fatal("older runs outside the last three are irrelevant")
	}
	_ = a
}

func pausedLens(t *testing.T, l *Ledger) []Topic {
	t.Helper()
	var all []Topic
	for _, s := range []string{"a", "b", "c"} {
		tps := fileRun(t, l, "lens", s)
		reject(t, l, tps...)
		all = append(all, tps...)
	}
	if !paused(t, l, "lens") {
		t.Fatal("setup: lens should be paused")
	}
	return all
}

func TestPauseClearsOnAdoptedDeferredReopenOrResume(t *testing.T) {
	cases := map[string]func(t *testing.T, l *Ledger, existing []Topic){
		"adopted on a new item": func(t *testing.T, l *Ledger, _ []Topic) {
			tp := newTopic(t, l, "lens", "x", false)
			if _, err := l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: RulingAdopted}); err != nil {
				t.Fatal(err)
			}
		},
		"deferred on a new item": func(t *testing.T, l *Ledger, _ []Topic) {
			tp := newTopic(t, l, "lens", "x", false)
			if _, err := l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: RulingDeferred}); err != nil {
				t.Fatal(err)
			}
		},
		"reopen": func(t *testing.T, l *Ledger, ex []Topic) {
			if _, err := l.Rule(ctx, Ruling{TopicID: ex[0].ID, Ruling: RulingReopen}); err != nil {
				t.Fatal(err)
			}
		},
		"resume record": func(t *testing.T, l *Ledger, _ []Topic) {
			if err := l.WithTx(ctx, func(tx *Tx) error { return tx.RecordResume(ctx, "lens") }); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, act := range cases {
		t.Run(name, func(t *testing.T) {
			l := open(t)
			ex := pausedLens(t, l)
			act(t, l, ex)
			if paused(t, l, "lens") {
				t.Fatal("pause must clear")
			}
			// the count restarts: three fresh all-Rejected runs pause again
			// (after a reopen the reopened item must be rejected too).
			reject(t, l, fileRun(t, l, "lens", "n1")...)
			reject(t, l, fileRun(t, l, "lens", "n2")...)
			if paused(t, l, "lens") {
				t.Fatal("two fresh runs must not pause")
			}
			reject(t, l, fileRun(t, l, "lens", "n3")...)
			if !paused(t, l, "lens") {
				t.Fatal("three fresh all-Rejected runs pause again")
			}
		})
	}
}

func TestOtherLensRulingDoesNotClearPause(t *testing.T) {
	l := open(t)
	pausedLens(t, l)
	tp := newTopic(t, l, "other", "x", false)
	if _, err := l.Rule(ctx, Ruling{TopicID: tp.ID, Ruling: RulingAdopted}); err != nil {
		t.Fatal(err)
	}
	if err := l.WithTx(ctx, func(tx *Tx) error { return tx.RecordResume(ctx, "other") }); err != nil {
		t.Fatal(err)
	}
	if !paused(t, l, "lens") {
		t.Fatal("rulings and resumes on another lens must not clear the pause")
	}
}

func TestRejectedRulingDoesNotClearPause(t *testing.T) {
	l := open(t)
	pausedLens(t, l)
	tp := newTopic(t, l, "lens", "x", false)
	reject(t, l, tp) // a Rejected ruling on an item that filed in no run
	if !paused(t, l, "lens") {
		t.Fatal("Rejected rulings keep the pause")
	}
}
