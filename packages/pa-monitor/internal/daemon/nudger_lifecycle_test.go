package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// captureSignaler is a fake signal.Signaler that records Send calls.
// It always claims to own every pid so it is selected by ResolveSignaler.
type captureSignaler struct {
	mu   sync.Mutex
	sent []capturedNudge
}

type capturedNudge struct {
	PID  int
	Text string
}

func (c *captureSignaler) Name() string       { return "capture" }
func (c *captureSignaler) Detect(_ int) bool  { return true }
func (c *captureSignaler) Send(pid int, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, capturedNudge{PID: pid, Text: text})
	return nil
}

func (c *captureSignaler) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

// Ensure captureSignaler satisfies signal.Signaler at compile time.
var _ signal.Signaler = (*captureSignaler)(nil)

// TestRunWith_NudgerFiresWithZeroGrace verifies end-to-end that RunWith
// wires the nudger correctly: with DisruptGrace=0, the disrupt producer
// fires on the second tick after the error is first seen.
func TestRunWith_NudgerFiresWithZeroGrace(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}
	runtimePath := filepath.Join(dir, "runtime.json")

	// Pre-write runtime.json with auto_resume_enabled=true.
	if err := WriteRuntimeState(runtimePath, RuntimeState{AutoResumeEnabled: true}); err != nil {
		t.Fatalf("WriteRuntimeState: %v", err)
	}

	errorAt := time.Now().Add(-1 * time.Second)

	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Path:  "/p1",
				IdleN: 1,
				Sessions: []*aggregate.SessionView{
					{
						Session: &session.Session{
							SessionID: "grace0-sid",
							PID:       88888,
							Status:    session.Idle,
						},
						SessionEnrichment: aggregate.SessionEnrichment{
							LastError: &transcript.ErrorRecord{
								Kind:        transcript.ErrUnknown,
								Text:        "API Error",
								At:          errorAt,
								IsTerminal:  true,
								IsRetryable: true,
							},
						},
					},
				},
			},
		},
	}

	cap := &captureSignaler{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths:       paths,
			Tick:        30 * time.Millisecond,
			RuntimePath: runtimePath,
			Poller: &stubPoller{
				snapshot: func(ctx context.Context) (*aggregate.Tree, bool, error) {
					return tree, true, nil
				},
			},
			NudgerSignalers:   []signal.Signaler{cap},
			DisruptGrace:      0, // fire immediately after first sighting
			EscalationAfter:   60 * time.Second,
			AutoResumeMessage: "continue",
		})
	}()

	waitForFile(t, paths.Socket)

	// Wait up to 500ms for at least one nudge.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cap.Len() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return after cancel")
	}

	if cap.Len() == 0 {
		t.Error("nudger did not fire any nudge within 500ms (expected DisruptProducer to fire after grace=0)")
	}
	// Verify the nudge was sent to the correct PID with correct text.
	cap.mu.Lock()
	first := cap.sent[0]
	cap.mu.Unlock()
	if first.PID != 88888 {
		t.Errorf("nudge PID = %d, want 88888", first.PID)
	}
	if first.Text != "continue" {
		t.Errorf("nudge text = %q, want \"continue\"", first.Text)
	}
}

// TestRunWith_NudgerNoOpWhenNotConfigured verifies that when RunWith is
// started without NudgerSignalers (and without RuntimePath), the daemon
// still processes ticks correctly and returns no error on cancellation.
// The nudger is nil in this configuration, so no nudges are sent.
func TestRunWith_NudgerNoOpWhenNotConfigured(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Sessions: []*aggregate.SessionView{
					{
						Session: &session.Session{
							SessionID: "no-nudger",
							PID:       1,
							Status:    session.Idle,
						},
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths: paths,
			Tick:  50 * time.Millisecond,
			Poller: &stubPoller{
				snapshot: func(ctx context.Context) (*aggregate.Tree, bool, error) {
					return tree, true, nil
				},
			},
			// NudgerSignalers intentionally absent — nudger should be nil.
		})
	}()

	waitForFile(t, paths.Socket)
	time.Sleep(100 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWith with no nudger returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return after cancel")
	}
}

// TestRunWith_NudgerAnnotatesPendingNudge verifies that the tree published
// to clients has PendingNudge set on a session whose nudge intent was
// reconciled in the same tick. With DisruptGrace=0 the intent is added to
// the store on tick 2 (tick 1 primes firstSeen). Annotation runs between
// Reconcile and Dispatch, so the TreeObserver receives the tree with
// PendingNudge set before (and on) the firing tick. This proves the
// Reconcile → annotate → Dispatch split works correctly.
func TestRunWith_NudgerAnnotatesPendingNudge(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}
	runtimePath := filepath.Join(dir, "runtime.json")

	if err := WriteRuntimeState(runtimePath, RuntimeState{AutoResumeEnabled: true}); err != nil {
		t.Fatalf("WriteRuntimeState: %v", err)
	}

	errorAt := time.Now().Add(-1 * time.Second)
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Path:  "/p1",
				IdleN: 1,
				Sessions: []*aggregate.SessionView{
					{
						Session: &session.Session{
							SessionID: "annotate-sid",
							PID:       77777,
							Status:    session.Idle,
						},
						SessionEnrichment: aggregate.SessionEnrichment{
							LastError: &transcript.ErrorRecord{
								Kind:        transcript.ErrUnknown,
								Text:        "API Error",
								At:          errorAt,
								IsTerminal:  true,
								IsRetryable: true,
							},
						},
					},
				},
			},
		},
	}

	// treesCh receives annotated tree snapshots from the TreeObserver.
	treesCh := make(chan *aggregate.Tree, 32)

	cap := &captureSignaler{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths:       paths,
			Tick:        30 * time.Millisecond,
			RuntimePath: runtimePath,
			Poller: &stubPoller{
				snapshot: func(ctx context.Context) (*aggregate.Tree, bool, error) {
					return tree, true, nil
				},
			},
			NudgerSignalers: []signal.Signaler{cap},
			// DisruptGrace=0: tick 1 primes firstSeen, tick 2 adds intent +
			// annotation runs before dispatch, so TreeObserver sees PendingNudge.
			DisruptGrace:      0,
			EscalationAfter:   60 * time.Second,
			AutoResumeMessage: "continue",
			TreeObserver: func(t *aggregate.Tree) {
				select {
				case treesCh <- t:
				default:
				}
			},
		})
	}()

	waitForFile(t, paths.Socket)

	// Wait up to 500ms for a tree where the session has PendingNudge set.
	// Tick 2 adds the intent, annotates, then dispatches. The TreeObserver
	// sees the annotated tree (PendingNudge is set on the sv struct; dispatch
	// only clears the store, not the struct field).
	deadline := time.Now().Add(500 * time.Millisecond)
	var foundPendingNudge bool
	for time.Now().Before(deadline) && !foundPendingNudge {
		select {
		case published := <-treesCh:
			for _, d := range published.Dirs {
				for _, sv := range d.Sessions {
					if sv.SessionID == "annotate-sid" && sv.PendingNudge != nil {
						for _, src := range sv.PendingNudge.Sources {
							if src == "disrupted" {
								foundPendingNudge = true
							}
						}
					}
				}
			}
		case <-time.After(50 * time.Millisecond):
			// no tree yet — keep polling
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return after cancel")
	}

	if !foundPendingNudge {
		t.Error("PendingNudge with source=disrupted was never set on the published tree (annotation before dispatch is broken)")
	}
}
