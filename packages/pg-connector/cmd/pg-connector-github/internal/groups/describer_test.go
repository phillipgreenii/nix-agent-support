package groups

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

func content(t *testing.T, s summary.Summary) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDescriber_FillsEveryPrimaryField(t *testing.T) {
	s := summary.Summary{
		NodeID: "PR_1", Repo: "Acme-Org/Widget", Number: 7, State: "OPEN",
		HeadRefOid: "head1", BaseRefOid: "base1", HeadRefName: "feature/x",
		UpdatedAt: "2026-10-12T09:30:00Z",
	}
	got, err := Describer{}.Describe(content(t, s))
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != "Acme-Org/Widget#7" || got.Stamp != "head1" || got.Stamp2 != "base1" {
		t.Errorf("Ref, Stamp, Stamp2 = %q, %q, %q", got.Ref, got.Stamp, got.Stamp2)
	}
	if want := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC); !got.OriginAt.Equal(want) {
		t.Errorf("OriginAt = %v, want %v", got.OriginAt, want)
	}
	if got.Gone || got.Terminal {
		t.Errorf("Gone = %v, Terminal = %v for an open pull request, want false, false", got.Gone, got.Terminal)
	}
	if got.Attrs[AttrHeadRef] != "feature/x" || got.Attrs[AttrState] != "OPEN" || len(got.Attrs) != 2 {
		t.Errorf("Attrs = %v, want the head branch and the state only", got.Attrs)
	}
}

func TestDescriber_OriginAtIsZeroUnlessUpdatedAtParses(t *testing.T) {
	for name, updated := range map[string]string{"empty": "", "garbage": "yesterday", "date only": "2026-10-12"} {
		t.Run(name, func(t *testing.T) {
			got, err := Describer{}.Describe(content(t, summary.Summary{Repo: "a/b", Number: 1, UpdatedAt: updated}))
			if err != nil {
				t.Fatal(err)
			}
			if !got.OriginAt.IsZero() {
				t.Errorf("OriginAt = %v for updated_at %q, want zero", got.OriginAt, updated)
			}
		})
	}
}

func TestDescriber_GoneSummaryHasNoReferenceAndIsGone(t *testing.T) {
	got, err := Describer{}.Describe(content(t, summary.Summary{NodeID: "PR_1", Gone: true, Repo: "a/b", Number: 3}))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Gone || got.Ref != "" {
		t.Errorf("Gone = %v, Ref = %q, want gone with no reference", got.Gone, got.Ref)
	}
}

func TestDescriber_TerminalIsMergedOrClosed(t *testing.T) {
	for state, want := range map[string]bool{"OPEN": false, "MERGED": true, "CLOSED": true, "": false} {
		got, err := Describer{}.Describe(content(t, summary.Summary{Repo: "a/b", Number: 1, State: state}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Terminal != want {
			t.Errorf("Terminal for state %q = %v, want %v", state, got.Terminal, want)
		}
		if got.Attrs[AttrState] != state {
			t.Errorf("Attrs[state] = %q, want %q", got.Attrs[AttrState], state)
		}
	}
}

func TestDescriber_ContentThatDoesNotDecodeIsAnError(t *testing.T) {
	if _, err := (Describer{}).Describe([]byte("not json")); err == nil {
		t.Error("a content that does not decode must be an error")
	}
}

func TestFoldRef(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Acme-Org/Widget#7", "acme-org/widget#7"},
		{"acme-org/widget#7", "acme-org/widget#7"},
		{"Acme-Org/Widget", "acme-org/widget"},
		{"Acme/Widget#7#X", "acme/widget#7#X"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := FoldRef(tc.in); got != tc.want {
			t.Errorf("FoldRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestErrGoneIsTheSharedSentinel(t *testing.T) {
	if ErrGone != groupsvc.ErrGone {
		t.Error("groups.ErrGone must be the shared groupsvc.ErrGone itself")
	}
	if !errors.Is(ErrGone, groupsvc.ErrGone) {
		t.Error("errors.Is(groups.ErrGone, groupsvc.ErrGone) = false")
	}
}
