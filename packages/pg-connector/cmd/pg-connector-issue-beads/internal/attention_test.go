package internal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func attnBead(id, title, status string, priority int, labels ...string) string {
	lj, _ := json.Marshal(labels)
	tj, _ := json.Marshal(title)
	return `{"id":"` + id + `","title":` + string(tj) + `,"status":"` + status + `","priority":` +
		string(rune('0'+priority)) + `,"issue_type":"task","labels":` + string(lj) + `}`
}

func attnCtx(labels ...string) context.Context {
	cfg, _ := json.Marshal(map[string]any{"attention_labels": labels})
	return scriptout.WithConfig(context.Background(), cfg)
}

func TestListAttention_EmptyOrMissingLabelsIsUnavailableBeforeAnyBdCall(t *testing.T) {
	cases := map[string]context.Context{
		"no config":    context.Background(),
		"empty list":   attnCtx(),
		"blank labels": attnCtx("", "  "),
		"wrong key":    scriptout.WithConfig(context.Background(), json.RawMessage(`{"activity_actors":["me"]}`)),
		"bad json":     scriptout.WithConfig(context.Background(), json.RawMessage(`not json`)),
		"wrong type":   scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_labels":"attention"}`)),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			fr := listFake(attnBead("tp-1", "x", "open", 1, "attention"))
			got, err := New(fr).ListAttention(ctx)
			if !errors.Is(err, scriptout.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if !strings.Contains(err.Error(), "attention_labels") {
				t.Errorf("err = %q, want it to name attention_labels", err)
			}
			if got != nil {
				t.Errorf("items = %v, want none (never an unscoped result)", got)
			}
			if len(fr.calls) != 0 {
				t.Errorf("bd calls = %v, want none", fr.calls)
			}
		})
	}
}

func TestListAttention_BdInvocationIsReadOnlyLabelAnyAndStatusScoped(t *testing.T) {
	fr := listFake()
	if _, err := New(fr).ListAttention(attnCtx("attention", "human-focus")); err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("bd calls = %v, want exactly one", fr.calls)
	}
	args := fr.calls[0]
	if args[0] != "list" {
		t.Errorf("args = %v, want a bd list call", args)
	}
	for _, want := range []string{
		"--readonly", "--json",
		"--label-any=attention,human-focus",
		"--status=open,in_progress,blocked",
	} {
		if !containsArg(args, want) {
			t.Errorf("args = %v, missing %q", args, want)
		}
	}
	// -n 0 lifts bd's default 50-row cap so the to-do list is never cut short.
	if !argsEndWith(args, "-n", "0") {
		t.Errorf("args = %v, want to end with -n 0", args)
	}
	if containsArg(args, "--all") {
		t.Errorf("args = %v, must not ask for closed beads (--all)", args)
	}
}

func TestListAttention_LabelWithCommaIsCSVQuoted(t *testing.T) {
	fr := listFake()
	if _, err := New(fr).ListAttention(attnCtx("a,b", "c")); err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if !containsArg(fr.calls[0], `--label-any="a,b",c`) {
		t.Errorf("args = %v, want CSV-quoted --label-any", fr.calls[0])
	}
}

func TestListAttention_DedupsAndTrimsConfiguredLabels(t *testing.T) {
	fr := listFake()
	if _, err := New(fr).ListAttention(attnCtx(" attention ", "attention", "", "human-focus")); err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if !containsArg(fr.calls[0], "--label-any=attention,human-focus") {
		t.Errorf("args = %v, want deduped trimmed labels", fr.calls[0])
	}
}

func TestListAttention_ItemShapeSeverityAndSummary(t *testing.T) {
	fr := listFake(attnBead("tp-1", "Decide the thing", "open", 1, "agent-support", "human-focus", "attention"))
	got, err := New(fr).ListAttention(attnCtx("attention", "human-focus"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("items = %+v, want 1", got)
	}
	it := got[0]
	if it.Type != "issue" || it.ID != "tp-1" {
		t.Errorf("identity = %q/%q, want issue/tp-1", it.Type, it.ID)
	}
	if it.Severity != schema.SeverityHigh {
		t.Errorf("severity = %q, want high (P1)", it.Severity)
	}
	if it.URL != "" || it.Group != nil {
		t.Errorf("url/group = %q/%v, want both omitted (no bd page, no grouping)", it.URL, it.Group)
	}
	// Summary carries the title, the priority and only the MATCHED labels (in
	// configured order), so the reason the bead is on the list is visible.
	want := "Decide the thing [P1; attention, human-focus]"
	if it.Summary != want {
		t.Errorf("summary = %q, want %q", it.Summary, want)
	}
}

func TestListAttention_SeverityMapping(t *testing.T) {
	cases := map[int]schema.Severity{
		0: schema.SeverityCritical,
		1: schema.SeverityHigh,
		2: schema.SeverityMedium,
		3: schema.SeverityLow,
		4: schema.SeverityLow,
	}
	for p, want := range cases {
		fr := listFake(attnBead("tp-1", "t", "open", p, "attention"))
		got, err := New(fr).ListAttention(attnCtx("attention"))
		if err != nil || len(got) != 1 {
			t.Fatalf("P%d: %v, %v", p, got, err)
		}
		if got[0].Severity != want {
			t.Errorf("P%d severity = %q, want %q", p, got[0].Severity, want)
		}
	}
}

func TestListAttention_StatusHandling(t *testing.T) {
	fr := listFake(
		attnBead("tp-open", "t", "open", 2, "attention"),
		attnBead("tp-prog", "t", "in_progress", 2, "attention"),
		attnBead("tp-blocked", "t", "blocked", 2, "attention"),
		attnBead("tp-deferred", "t", "deferred", 2, "attention"),
		attnBead("tp-closed", "t", "closed", 2, "attention"),
		attnBead("tp-pinned", "t", "pinned", 2, "attention"),
	)
	got, err := New(fr).ListAttention(attnCtx("attention"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	var ids []string
	for _, it := range got {
		ids = append(ids, it.ID)
	}
	if want := "tp-blocked,tp-open,tp-prog"; strings.Join(ids, ",") != want {
		t.Errorf("ids = %v, want %s (open, in_progress, blocked only; deferred/closed/pinned dropped)", ids, want)
	}
}

func TestListAttention_DropsBeadsWithoutAConfiguredLabel(t *testing.T) {
	// Defense in depth: even if bd ignored --label-any, an unlabelled bead is
	// never reported.
	fr := listFake(
		attnBead("tp-yes", "t", "open", 2, "human-focus"),
		attnBead("tp-no", "t", "open", 2, "other"),
		attnBead("tp-none", "t", "open", 2),
	)
	got, err := New(fr).ListAttention(attnCtx("attention", "human-focus"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(got) != 1 || got[0].ID != "tp-yes" {
		t.Fatalf("items = %+v, want only tp-yes", got)
	}
}

func TestListAttention_OrderIsPriorityThenID(t *testing.T) {
	fr := listFake(
		attnBead("tp-3", "t", "open", 3, "attention"),
		attnBead("tp-b", "t", "open", 1, "attention"),
		attnBead("tp-0", "t", "open", 0, "attention"),
		attnBead("tp-a", "t", "open", 1, "attention"),
	)
	got, err := New(fr).ListAttention(attnCtx("attention"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	var ids []string
	for _, it := range got {
		ids = append(ids, it.ID)
	}
	if want := "tp-0,tp-a,tp-b,tp-3"; strings.Join(ids, ",") != want {
		t.Errorf("ids = %v, want %s", ids, want)
	}
}

func TestListAttention_EmptyResultIsNonNilEmptyList(t *testing.T) {
	got, err := New(listFake()).ListAttention(attnCtx("attention"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("items = %#v, want a non-nil empty list (marshals as [], not null)", got)
	}
}

func TestListAttention_BdFailureIsClassified(t *testing.T) {
	fr := &fakeRunner{handle: func([]string) (string, error) {
		return "", errors.New("bd: connection refused")
	}}
	_, err := New(fr).ListAttention(attnCtx("attention"))
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestListAttention_WorkspaceNotConfiguredIsUnavailable(t *testing.T) {
	fr := &fakeRunner{handle: func([]string) (string, error) {
		return "", ErrWorkspaceNotConfigured
	}}
	_, err := New(fr).ListAttention(attnCtx("attention"))
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
