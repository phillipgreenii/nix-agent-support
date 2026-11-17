package supervisor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
)

var (
	statusT0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	exDaemon = DaemonStatus{
		PID:             4242,
		Version:         "26.11.17.00000+example",
		StartedAt:       statusT0.Add(-time.Hour),
		ConfigPath:      "/example/config.toml",
		ConfigHash:      "0123abcd",
		ControlStateDir: "/example/control",
		ClaudeConfigDir: "/example/claude",
		ConfigProblems:  []string{"tick: \"0s\" must be positive; using the default 1m0s"},
	}
)

// fullStatus returns an AssistantStatus with every field set.
func fullStatus(id string) AssistantStatus {
	return AssistantStatus{
		ID:              id,
		Enabled:         true,
		Running:         true,
		PID:             101,
		SessionID:       "example-session-" + id,
		StartedAt:       statusT0.Add(-30 * time.Minute),
		ContextTokens:   162000,
		ContextFraction: 0.81,
		ContextAt:       statusT0.Add(-time.Minute),
		Ladder:          "meter",
		Rung:            "nudge",
		Reason:          "context at 81% (162000 of 200000 tokens)",
		WaitUntil:       statusT0.Add(5 * time.Minute),
		WaitReason:      "backoff",
		Hint:            "did not exit after the nudge (a permission prompt?)",
		LastError:       "pg-claude-control usage: no control mod",
		ConfigError:     "config error: assistant[" + id + "].tick: bad",
		LastExit:        &LastExit{Status: 3, Signal: 15, UptimeS: 42.5},
		LastTick:        statusT0,
	}
}

// zeroFields names the fields of v (a struct) holding their zero value, so a
// field added later without a test value fails the round-trip test.
func zeroFields(v any) []string {
	rv := reflect.ValueOf(v)
	var out []string
	for i := range rv.NumField() {
		if rv.Field(i).IsZero() {
			out = append(out, rv.Type().Field(i).Name)
		}
	}
	return out
}

func TestStatusBoardFlush(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	b := NewStatusBoard(path, exDaemon)

	// a sets every field; b differs, with false booleans.
	a, bst := fullStatus("example-a"), fullStatus("example-b")
	bst.Enabled, bst.Running, bst.Ladder, bst.Rung, bst.WaitReason = false, false, "disabled", "kill", "block_hold"
	if z := zeroFields(a); len(z) > 0 {
		t.Fatalf("fixture %s leaves fields unset: %v", a.ID, z)
	}
	if z := zeroFields(exDaemon); len(z) > 0 {
		t.Fatalf("daemon fixture leaves fields unset: %v", z)
	}
	blocks := []control.Limit{
		{Kind: control.KindFiveHour, Fraction: 0.96, ResetsAt: statusT0.Add(3 * time.Hour), ObservedAt: statusT0.Add(-time.Minute)},
		{Kind: control.KindSevenDay, Fraction: 0.40, ObservedAt: statusT0.Add(-2 * time.Minute)},
	}

	b.Set(bst) // out of order: the file lists assistants by id
	b.Set(a)
	b.SetOrphans([]string{"example-gone"})
	b.SetBlocks(blocks)
	if err := b.Flush(statusT0); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "status.json" {
		t.Fatalf("dir holds %v, want only status.json", names(entries))
	}
	assertMode(t, path, 0o600)

	got, err := ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Status{
		SchemaVersion: 1,
		WrittenAt:     statusT0,
		Daemon:        exDaemon,
		Assistants:    []AssistantStatus{a, bst},
		Orphans:       []string{"example-gone"},
		Blocks:        blocks,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadStatus =\n  %+v\nwant\n  %+v", got, want)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"schema_version": 1`, `"observed_at"`, `"pg_claude_control_state_dir"`, `"last_exit"`, `"uptime_s"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("status.json lacks %s:\n%s", key, raw)
		}
	}
}

// A second Set for an id replaces the first; an empty board writes empty
// lists, not null.
func TestStatusBoardSetReplacesAndEmptyLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	b := NewStatusBoard(path, DaemonStatus{PID: 1})
	if err := b.Flush(statusT0); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"assistants": []`, `"blocks": []`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("empty status.json lacks %s:\n%s", key, raw)
		}
	}
	if strings.Contains(string(raw), `"orphans"`) {
		t.Errorf("empty status.json carries orphans:\n%s", raw)
	}

	b.Set(AssistantStatus{ID: "example-a", Reason: "first"})
	b.Set(AssistantStatus{ID: "example-a", Reason: "second"})
	if err := b.Flush(statusT0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Assistants) != 1 || got.Assistants[0].Reason != "second" {
		t.Errorf("assistants = %+v, want one with reason second", got.Assistants)
	}
	if !got.WrittenAt.Equal(statusT0.Add(time.Second)) {
		t.Errorf("written_at = %v, want %v", got.WrittenAt, statusT0.Add(time.Second))
	}
}

// The board keeps its own copies: a caller changing what it passed in
// changes nothing on the board.
func TestStatusBoardCopiesInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	d := DaemonStatus{PID: 1, ConfigProblems: []string{"p1"}}
	b := NewStatusBoard(path, d)
	s := AssistantStatus{ID: "example-a", LastExit: &LastExit{Status: 3}}
	orphans := []string{"example-gone"}
	limits := []control.Limit{{Kind: control.KindFiveHour, Fraction: 0.5, ObservedAt: statusT0}}
	b.Set(s)
	b.SetOrphans(orphans)
	b.SetBlocks(limits)

	d.ConfigProblems[0] = "changed"
	s.LastExit.Status = 9
	orphans[0] = "changed"
	limits[0].Fraction = 0.9

	if err := b.Flush(statusT0); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Daemon.ConfigProblems[0] != "p1" || got.Assistants[0].LastExit.Status != 3 ||
		got.Orphans[0] != "example-gone" || got.Blocks[0].Fraction != 0.5 {
		t.Errorf("board shares the caller's values: %+v", got)
	}
}

func TestStatusBoardConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	b := NewStatusBoard(path, exDaemon)
	var wg sync.WaitGroup
	for i := range 8 {
		id := fmt.Sprintf("example-%d", i)
		wg.Go(func() {
			for j := range 50 {
				s := fullStatus(id)
				s.ContextTokens = int64(j)
				b.Set(s)
				b.SetOrphans([]string{"example-gone-" + id})
				b.SetBlocks([]control.Limit{{Kind: control.KindFiveHour, Fraction: 0.1, ObservedAt: statusT0}})
				if err := b.Flush(statusT0.Add(time.Duration(j) * time.Second)); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if err := b.Flush(statusT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Assistants) != 8 {
		t.Fatalf("assistants = %d, want 8", len(got.Assistants))
	}
	for _, s := range got.Assistants {
		if s.ContextTokens != 49 {
			t.Errorf("%s context_tokens = %d, want the last Set's 49", s.ID, s.ContextTokens)
		}
	}
}

// No Flush waits on another's file write, and a state change flushed while a
// write is in progress still reaches the file: the writer in progress writes
// it next, so the last write to complete (what the file holds) carries it.
// The write is replaced with one whose first call blocks until released;
// each write is recorded when it completes.
//
// A regression here blocks on the board's mutex, which testing/synctest does
// not count as durably blocked, so synctest cannot turn the hang into a
// failure. A real-time watchdog does: if the second Set and Flush have not
// returned within 10s, it releases the blocked write and fails the test.
func TestFlushNeverWaitsOnAnotherWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	b := NewStatusBoard(path, DaemonStatus{PID: 1})
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	var written []Status
	b.write = func(p string, s Status) error {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
		mu.Lock()
		written = append(written, s)
		mu.Unlock()
		return nil
	}

	b.Set(AssistantStatus{ID: "example-a", Reason: "before"})
	firstDone := make(chan error, 1)
	go func() { firstDone <- b.Flush(statusT0) }()
	<-entered

	// While the first write is blocked, Set and Flush return at once.
	secondDone := make(chan error, 1)
	go func() {
		b.Set(AssistantStatus{ID: "example-a", Reason: "during"})
		secondDone <- b.Flush(statusT0.Add(time.Second))
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Errorf("second Flush: %v", err)
		}
		close(release)
	case <-time.After(10 * time.Second):
		close(release) // unblock the writer so every goroutine can finish
		<-secondDone
		<-firstDone
		t.Fatal("Set or Flush waited for another Flush's write")
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first Flush: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(written) != 2 {
		t.Fatalf("writes = %d, want 2 (the second state written by the first writer)", len(written))
	}
	if r := written[0].Assistants[0].Reason; r != "before" {
		t.Errorf("first write reason = %q, want before", r)
	}
	if r := written[1].Assistants[0].Reason; r != "during" || !written[1].WrittenAt.Equal(statusT0.Add(time.Second)) {
		t.Errorf("second write = %q at %v, want during at %v", r, written[1].WrittenAt, statusT0.Add(time.Second))
	}
}

// A write that panics leaves the board able to write: the next Flush writes.
func TestFlushWritesAfterAPanickingWrite(t *testing.T) {
	b := NewStatusBoard(filepath.Join(t.TempDir(), "status.json"), DaemonStatus{PID: 1})
	calls := 0
	b.write = func(string, Status) error {
		calls++
		if calls == 1 {
			panic("example write panic")
		}
		return nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the first Flush did not panic")
			}
		}()
		_ = b.Flush(statusT0)
	}()
	if err := b.Flush(statusT0); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("writes = %d, want 2: the board stayed marked as writing", calls)
	}
}

// A write error is returned, and the next Flush tries again.
func TestFlushReportsWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-parent", "x", "status.json")
	if err := os.WriteFile(filepath.Dir(filepath.Dir(path)), nil, 0o600); err != nil {
		t.Fatal(err) // a file where the directory would go
	}
	b := NewStatusBoard(path, DaemonStatus{PID: 1})
	if err := b.Flush(statusT0); err == nil {
		t.Fatal("Flush over a file in the path succeeded")
	}
	if err := os.Remove(filepath.Dir(filepath.Dir(path))); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(statusT0); err != nil {
		t.Fatalf("Flush after the path was fixed: %v", err)
	}
}

func TestReadStatusErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadStatus(filepath.Join(dir, "absent.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v, want fs.ErrNotExist", err)
	}
	for name, body := range map[string]string{
		"garbage":       "not json",
		"wrong schema":  `{"schema_version": 2}`,
		"no schema":     `{"daemon": {"pid": 1}}`,
		"trailing data": `{"schema_version": 1} {}`,
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadStatus(p); err == nil {
			t.Errorf("%s: ReadStatus succeeded", name)
		} else if !strings.Contains(err.Error(), p) {
			t.Errorf("%s: error %q does not name the file", name, err)
		}
	}
}

func names(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}
