package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// The busy state in the TUI (bead pg2-8ij5o, operator ruling 30 of docket pg2-12gr4).
// The TUI shows the core's own `busy` verdict as given: the core derives it from the
// decline kind and the in-flight state only, never from a decline's reason.

func TestStatusReply_DecodesBusy(t *testing.T) {
	var r StatusReply
	payload := `{"listeners":[{"role":"a","busy":true},{"role":"b"}],"sources":[{"name":"s","busy":true},{"name":"t"}]}`
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Listeners[0].Busy || r.Listeners[1].Busy {
		t.Errorf("listener busy = %v, %v, want true, false", r.Listeners[0].Busy, r.Listeners[1].Busy)
	}
	if !r.Sources[0].Busy || r.Sources[1].Busy {
		t.Errorf("source busy = %v, %v, want true, false", r.Sources[0].Busy, r.Sources[1].Busy)
	}
}

// A busy role that is not processing (declined busy since its last progress) shows
// [busy]; a processing one keeps its [processing] marker alone; an idle one shows
// neither. The suffix rides on the health text, orthogonal to the ranking.
func TestHealthText_BusySuffix(t *testing.T) {
	theme := render.NewTheme(false)
	now := time.Now()
	minuteMs := time.Minute.Milliseconds()

	idle := listenerHealthText(Listener{Enabled: true}, theme)
	if strings.Contains(idle, "busy") || strings.Contains(idle, "processing") {
		t.Errorf("idle listener health = %q, want no busy or processing marker", idle)
	}
	declined := listenerHealthText(Listener{Enabled: true, Busy: true}, theme)
	if declined != idle+" [busy]" {
		t.Errorf("busy listener health = %q, want %q", declined, idle+" [busy]")
	}
	flying := listenerHealthText(Listener{Enabled: true, Busy: true, InFlight: true}, theme)
	if flying != idle+" [processing]" {
		t.Errorf("processing listener health = %q, want %q", flying, idle+" [processing]")
	}

	idleSrc := sourceHealthText(Source{Enabled: true, LastTick: now, ExpectedIntervalMs: minuteMs}, now, theme)
	busySrc := sourceHealthText(Source{Enabled: true, LastTick: now, ExpectedIntervalMs: minuteMs, Busy: true, InFlight: true}, now, theme)
	if busySrc != idleSrc+" [processing]" {
		t.Errorf("busy source health = %q, want %q", busySrc, idleSrc+" [processing]")
	}
	if got := sourceHealthText(Source{Enabled: true, LastTick: now, ExpectedIntervalMs: minuteMs, Busy: true}, now, theme); got != idleSrc+" [busy]" {
		t.Errorf("busy-only source health = %q, want %q", got, idleSrc+" [busy]")
	}
}

func TestRenderDetails_ShowBusy(t *testing.T) {
	theme := render.NewTheme(false)
	now := time.Now()
	if got := renderListenerDetail(Listener{Role: "r", Enabled: true, Busy: true}, theme, nil); !strings.Contains(got, "Busy:           yes\n") {
		t.Errorf("busy listener detail = %q, want a Busy line showing yes", got)
	}
	if got := renderListenerDetail(Listener{Role: "r", Enabled: true}, theme, nil); !strings.Contains(got, "Busy:           no\n") {
		t.Errorf("idle listener detail = %q, want a Busy line showing no", got)
	}
	if got := renderSourceDetail(Source{Name: "s", Enabled: true, Busy: true}, now, theme, nil); !strings.Contains(got, "Busy:          yes\n") {
		t.Errorf("busy source detail = %q, want a Busy line showing yes", got)
	}
	if got := renderSourceDetail(Source{Name: "s", Enabled: true}, now, theme, nil); !strings.Contains(got, "Busy:          no\n") {
		t.Errorf("idle source detail = %q, want a Busy line showing no", got)
	}
}
