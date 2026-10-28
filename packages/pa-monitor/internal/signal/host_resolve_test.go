package signal_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// countingSignaler counts Detect calls so a test can prove which signalers a
// host-aware resolution consulted. Name is the host name it answers to.
type countingSignaler struct {
	name    string
	detect  bool
	detects int
}

func (c *countingSignaler) Name() string { return c.name }
func (c *countingSignaler) Detect(int) bool {
	c.detects++
	return c.detect
}
func (c *countingSignaler) Send(int, string) error { return nil }

// ancestrySignaler is a countingSignaler whose Detect is exactly the ancestry
// predicate that classifies the host (as CmuxSignaler's is).
type ancestrySignaler struct{ countingSignaler }

func (a *ancestrySignaler) DetectIsHostClassification() bool { return true }

type inspectingSignaler struct {
	countingSignaler
	inspected int
	unsub     bool
}

func (i *inspectingSignaler) HasUnsubmittedInput(int) bool {
	i.inspected++
	return i.unsub
}

func TestBareHost(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "",
		"cmux":                       "cmux",
		"cmux (no bridge)":           "cmux",
		"cmux (bridge disconnected)": "cmux",
		"tmux":                       "tmux",
		"unknown":                    "unknown",
	} {
		if got := signal.BareHost(in); got != want {
			t.Errorf("BareHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveSignalerForHost_CmuxHostNeverConsultsTmux(t *testing.T) {
	tmux := &countingSignaler{name: "tmux"}
	cmux := &ancestrySignaler{countingSignaler{name: "cmux", detect: true}}
	got := signal.ResolveSignalerForHost([]signal.Signaler{tmux, cmux}, "cmux (no bridge)", 7)
	if got != signal.Signaler(cmux) {
		t.Fatalf("resolved %v, want the cmux signaler", got)
	}
	if tmux.detects != 0 {
		t.Errorf("tmux Detect called %d times for a cmux-hosted session, want 0", tmux.detects)
	}
	if cmux.detects != 0 {
		t.Errorf("cmux Detect called %d times; the cached cmux host already proves it, want 0", cmux.detects)
	}
}

func TestResolveSignalerForHost_TmuxHostWalksOnlyTmux(t *testing.T) {
	tmux := &countingSignaler{name: "tmux", detect: true}
	cmux := &ancestrySignaler{countingSignaler{name: "cmux", detect: true}}
	got := signal.ResolveSignalerForHost([]signal.Signaler{cmux, tmux}, "tmux", 7)
	if got != signal.Signaler(tmux) {
		t.Fatalf("resolved %v, want tmux", got)
	}
	if tmux.detects != 1 || cmux.detects != 0 {
		t.Errorf("detects tmux=%d cmux=%d, want 1 and 0", tmux.detects, cmux.detects)
	}
}

func TestResolveSignalerForHost_TmuxHostWithoutPaneIsSurfaceless(t *testing.T) {
	tmux := &countingSignaler{name: "tmux", detect: false}
	cmux := &ancestrySignaler{countingSignaler{name: "cmux", detect: true}}
	if got := signal.ResolveSignalerForHost([]signal.Signaler{tmux, cmux}, "tmux", 7); got != nil {
		t.Fatalf("resolved %v, want nil (matching signaler no longer detects pid)", got)
	}
	if cmux.detects != 0 {
		t.Errorf("cmux consulted %d times for a tmux-hosted pid, want 0", cmux.detects)
	}
}

func TestResolveSignalerForHost_UnknownOrEmptyFallsBackToFullWalk(t *testing.T) {
	for _, host := range []string{"", "unknown", "ghostty"} {
		tmux := &countingSignaler{name: "tmux"}
		cmux := &ancestrySignaler{countingSignaler{name: "cmux", detect: true}}
		got := signal.ResolveSignalerForHost([]signal.Signaler{tmux, cmux}, host, 7)
		if got != signal.Signaler(cmux) {
			t.Errorf("host %q: resolved %v, want the legacy first-match (cmux)", host, got)
		}
		if tmux.detects != 1 || cmux.detects != 1 {
			t.Errorf("host %q: detects tmux=%d cmux=%d, want the full walk 1 and 1", host, tmux.detects, cmux.detects)
		}
	}
}

func TestHasUnsubmittedInputForHost(t *testing.T) {
	t.Run("tmux host inspects only tmux, without a second Detect", func(t *testing.T) {
		tmux := &inspectingSignaler{countingSignaler: countingSignaler{name: "tmux", detect: true}, unsub: false}
		cmux := &ancestrySignaler{countingSignaler{name: "cmux", detect: true}}
		if signal.HasUnsubmittedInputForHost([]signal.Signaler{cmux, tmux}, "tmux", 7) {
			t.Error("want false (inspector reported empty input box)")
		}
		if tmux.inspected != 1 || tmux.detects != 0 || cmux.detects != 0 {
			t.Errorf("inspected=%d tmuxDetects=%d cmuxDetects=%d, want 1/0/0", tmux.inspected, tmux.detects, cmux.detects)
		}
	})
	t.Run("cmux host fails closed with no walk at all", func(t *testing.T) {
		tmux := &inspectingSignaler{countingSignaler: countingSignaler{name: "tmux", detect: true}}
		cmux := &ancestrySignaler{countingSignaler{name: "cmux", detect: true}}
		if !signal.HasUnsubmittedInputForHost([]signal.Signaler{tmux, cmux}, "cmux", 7) {
			t.Error("cmux has no inspector: must fail closed (true)")
		}
		if tmux.detects+cmux.detects != 0 {
			t.Errorf("Detect calls = %d, want 0", tmux.detects+cmux.detects)
		}
	})
	t.Run("unknown host keeps the legacy fail-closed resolution", func(t *testing.T) {
		if !signal.HasUnsubmittedInputForHost(nil, "unknown", 7) {
			t.Error("no signaler resolves: must fail closed (true)")
		}
	})
}

// A real CmuxSignaler must advertise that its Detect is the host classification
// (the property ResolveSignalerForHost relies on to skip the walk).
func TestCmuxSignaler_DetectIsHostClassification(t *testing.T) {
	var s signal.Signaler = &signal.CmuxSignaler{RunCmd: func(context.Context, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("no exec expected")
	}}
	hc, ok := s.(interface{ DetectIsHostClassification() bool })
	if !ok || !hc.DetectIsHostClassification() {
		t.Fatal("CmuxSignaler must implement DetectIsHostClassification() == true")
	}
	var tm signal.Signaler = &signal.TmuxSignaler{}
	if _, ok := tm.(interface{ DetectIsHostClassification() bool }); ok {
		t.Fatal("TmuxSignaler.Detect consults the live pane map; it must NOT claim to be the host classification")
	}
}
