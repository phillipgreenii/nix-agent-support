package store

import "testing"

// TestSyncRetryRoundTrip: the retry indicator (bead pg2-xb6fs) is readable
// back from the store exactly as written, under its documented meta key, and
// is removed by DeleteSyncRetry.
func TestSyncRetryRoundTrip(t *testing.T) {
	s := OpenForTest(t)

	if _, found, err := s.GetSyncRetry("o/r#1"); err != nil || found {
		t.Fatalf("GetSyncRetry on a fresh store: found=%v err=%v, want not found", found, err)
	}

	want := SyncRetry{
		Attempts: 3, MaxRetries: 10, State: SyncRetryRetrying,
		LastFailedAt: "2026-09-30T12:00:00Z", NextRetryAt: "2026-09-30T12:04:00Z",
	}
	if err := s.SetSyncRetry("o/r#1", want); err != nil {
		t.Fatalf("SetSyncRetry: %v", err)
	}
	got, found, err := s.GetSyncRetry("o/r#1")
	if err != nil || !found {
		t.Fatalf("GetSyncRetry: found=%v err=%v", found, err)
	}
	if got != want {
		t.Fatalf("GetSyncRetry = %+v, want %+v", got, want)
	}
	raw, found, err := s.GetMeta("sync_retry.o/r#1")
	if err != nil || !found || raw == "" {
		t.Fatalf("meta key sync_retry.o/r#1: found=%v err=%v raw=%q", found, err, raw)
	}

	if err := s.DeleteSyncRetry("o/r#1"); err != nil {
		t.Fatalf("DeleteSyncRetry: %v", err)
	}
	if _, found, err := s.GetSyncRetry("o/r#1"); err != nil || found {
		t.Fatalf("after delete: found=%v err=%v, want not found", found, err)
	}
	// Deleting absent state is not an error.
	if err := s.DeleteSyncRetry("o/r#1"); err != nil {
		t.Fatalf("DeleteSyncRetry (absent): %v", err)
	}
}

func TestSyncRetryCorruptValueIsAnError(t *testing.T) {
	s := OpenForTest(t)
	if err := s.SetMeta(SyncRetryKey("o/r#2"), "{not json"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if _, _, err := s.GetSyncRetry("o/r#2"); err == nil {
		t.Fatal("GetSyncRetry on a corrupt value: want an error")
	}
}

func TestSyncRetryRetriesAndEffectiveState(t *testing.T) {
	cases := []struct {
		r           SyncRetry
		wantRetries int
		wantState   string
	}{
		{SyncRetry{}, 0, SyncRetryRetrying}, // no recorded state: due, reads as retrying
		{SyncRetry{Attempts: 1, State: SyncRetryRetrying}, 0, SyncRetryRetrying},
		{SyncRetry{Attempts: 11, State: SyncRetryExhausted}, 10, SyncRetryExhausted},
		{SyncRetry{Attempts: 1, State: SyncRetryNonTransient}, 0, SyncRetryNonTransient},
	}
	for _, c := range cases {
		if got := c.r.Retries(); got != c.wantRetries {
			t.Errorf("%+v.Retries() = %d, want %d", c.r, got, c.wantRetries)
		}
		if got := c.r.EffectiveState(); got != c.wantState {
			t.Errorf("%+v.EffectiveState() = %q, want %q", c.r, got, c.wantState)
		}
	}
}

func TestDeleteMeta(t *testing.T) {
	s := OpenForTest(t)
	if err := s.SetMeta("k", "v"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := s.DeleteMeta("k"); err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}
	if _, found, err := s.GetMeta("k"); err != nil || found {
		t.Fatalf("after DeleteMeta: found=%v err=%v", found, err)
	}
}
