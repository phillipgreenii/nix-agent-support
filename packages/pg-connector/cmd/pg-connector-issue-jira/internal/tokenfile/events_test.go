package tokenfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func eventStore(t *testing.T) (*Store, *[]string) {
	t.Helper()
	tmp, state := t.TempDir(), t.TempDir()
	for _, d := range []string{tmp, state} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(tmp, filepath.Join(state, "jira-token"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	s.SetObserver(func(e string) { got = append(got, e) })
	return s, &got
}

func TestStoreEventsAtEachTransition(t *testing.T) {
	s, got := eventStore(t)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("a clean start emitted %v", *got)
	}
	if err := s.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte("b")); err != nil { // the link is already right
		t.Fatal(err)
	}
	if err := s.Touch(); err != nil {
		t.Fatal(err)
	}
	if want := EventLinkWritten + "," + EventTouched; strings.Join(*got, ",") != want {
		t.Fatalf("events = %v, want %s", *got, want)
	}
	// A leftover from an earlier run is removed at the next start.
	*got = nil
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*got, ",") != EventStaleRemoved {
		t.Fatalf("start over a leftover emitted %v", *got)
	}
	*got = nil
	if err := s.Write([]byte("c")); err != nil {
		t.Fatal(err)
	}
	*got = nil
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*got, ",") != EventRemoved {
		t.Fatalf("remove emitted %v", *got)
	}
}

func TestAFailedRemoveEmitsRemoveFailed(t *testing.T) {
	s, got := eventStore(t)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	// Make the subdirectory unwritable so deleting its entries fails.
	if err := os.Chmod(s.subdir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.subdir, 0o700) })
	*got = nil
	if err := s.Remove(); err == nil {
		t.Skip("running as a user that ignores directory permissions")
	}
	if strings.Join(*got, ",") != EventRemoveFailed {
		t.Fatalf("failed remove emitted %v", *got)
	}
}

func TestAStoreWithoutAnObserverStillWorks(t *testing.T) {
	s, _ := eventStore(t)
	s.SetObserver(nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
}
