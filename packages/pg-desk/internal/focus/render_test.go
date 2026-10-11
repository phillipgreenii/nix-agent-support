package focus

import (
	"strings"
	"testing"
)

// Tests of RenderChanges and RenderEcho: the words come from BeadStates (one
// table), the cause words are the display words of the four stored causes.

func TestCauseDisplayIsExhaustiveOverTheStoredCauses(t *testing.T) {
	cases := []struct {
		cause string
		facts Facts
		want  string
	}{
		{CauseOperator, Facts{}, "you"},
		{CauseCap, Facts{}, "cap"},
		{CauseDropped, Facts{Hidden: true}, "no longer a candidate: hidden"},
		{CauseDropped, Facts{Inactive: true}, "no longer a candidate: inactive"},
		{CauseNewPeriod, Facts{}, "new plan"},
		{"", Facts{}, ""},
	}
	for _, c := range cases {
		if got := causeText(c.cause, c.facts); got != c.want {
			t.Errorf("causeText(%q, %+v) = %q, want %q", c.cause, c.facts, got, c.want)
		}
	}
	if got := (ChangeRow{}).CauseDisplay(); got != "-" {
		t.Errorf("a selection shows %q, want -", got)
	}
}

// TestRendererReadsTheTableShowReads checks that the words the CHANGES block
// prints for every state of the bead-state table are the table's own strike
// and select echo (the claimed and is-a-bead rows carry their extras), so the
// echo and show cannot drift: there is one definition.
func TestRendererReadsTheTableShowReads(t *testing.T) {
	for _, row := range BeadStates {
		for _, dir := range []string{DirectionSelect, DirectionRemove} {
			want := row.SelectEcho
			if dir == DirectionRemove {
				want = row.StrikeEcho
			}
			got := consequencePhrase(dir, row.State, "", want, "bd-1")
			switch {
			case row.State == StateNotApplicable:
				if !strings.HasPrefix(got, "is bead bd-1") {
					t.Errorf("%s %s = %q, want the is-a-bead phrase", dir, row.State, got)
				}
			case row.State == StateClaimed && dir == DirectionRemove:
				if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "(stop it with bd; pg-desk cannot)") {
					t.Errorf("%s %s = %q", dir, row.State, got)
				}
			default:
				if got != want {
					t.Errorf("%s %s = %q, want the table's %q", dir, row.State, got, want)
				}
			}
			out := RenderChanges(PreviewResult{Changes: []ChangeRow{{
				Direction: dir, Key: Key{Type: "pr", ID: "1"}, Title: "t", Echo: got, State: row.State,
			}}})
			if !strings.Contains(out, got) {
				t.Errorf("CHANGES lacks %q:\n%s", got, out)
			}
		}
	}
}

func TestRenderChangesEmptyAndNeverClosed(t *testing.T) {
	if got := RenderChanges(PreviewResult{}); got != "CHANGES\n  (none)\n" {
		t.Errorf("empty = %q", got)
	}
	got := RenderChanges(PreviewResult{NeverClosed: []string{"2026-10-08"}})
	if !strings.Contains(got, "period 2026-10-08 was never closed; its rows are deleted by this lock\n") {
		t.Errorf("never closed = %q", got)
	}
}

func TestRenderEchoMintNoteAndSkipsAreSeparate(t *testing.T) {
	res := PreviewResult{Echo: []EchoRow{
		{Kind: EchoSelected, Token: "ok", Key: Key{Type: "pr", ID: "1"}, Title: "alpha", Detail: "selected from the draft", Effect: "mint", Consequence: "will mint"},
		{Kind: EchoSkipped, Key: Key{Type: "pr", ID: "2"}, Title: "bravo", Detail: SkipFinished},
	}}
	echo := RenderEcho(res)
	if !strings.Contains(echo, "will mint (the router creates it on its next run; show reads pending until then)") {
		t.Errorf("no mint note:\n%s", echo)
	}
	if strings.Contains(echo, "bravo") {
		t.Errorf("a routine skip is in the echo table:\n%s", echo)
	}
	if got := RenderSkips(res); got != "skipped pr:2: finished\n" {
		t.Errorf("skips = %q", got)
	}
	if got := RenderEcho(PreviewResult{}); !strings.Contains(got, "(nothing applied)") {
		t.Errorf("empty echo = %q", got)
	}
}
