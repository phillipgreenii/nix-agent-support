package entity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// countingGH is a fake gh runner that records every call and answers from a
// canned function.
type countingGH struct {
	calls  [][]string
	answer func(args []string) ([]byte, error)
}

func (g *countingGH) Run(_ context.Context, args ...string) ([]byte, error) {
	g.calls = append(g.calls, args)
	return g.answer(args)
}

func newResolver(t *testing.T, gh *countingGH) (*Resolver, *governor.Governor) {
	t.Helper()
	clk := governor.NewManualClock(t0)
	gov, err := governor.New(ratefeed.GovernorConfig(config.Defaults().Settings), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	return &Resolver{
		Identity: NewIdentity(openStore(t), clk), GH: gh, Governor: gov, Feed: ratefeed.New(gov, gh, clk),
	}, gov
}

var interactive = ratefeed.Meta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive}

const rl = `"rateLimit":{"cost":1,"remaining":4900,"resetAt":"2026-10-12T10:00:00Z"}`

// argValue returns the value of the -f/-F flag named key.
func argValue(args []string, key string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if (args[i] == "-f" || args[i] == "-F") && strings.HasPrefix(args[i+1], key+"=") {
			return strings.TrimPrefix(args[i+1], key+"="), true
		}
	}
	return "", false
}

func TestResolve_FirstSightingsAreOneDocument(t *testing.T) {
	gh := &countingGH{answer: func(args []string) ([]byte, error) {
		return []byte(`{"data":{` + rl + `,
		  "p0":{"nameWithOwner":"Acme-Org/Widget","pullRequest":{"id":"PR_a","number":3}},
		  "p1":{"nameWithOwner":"acme-org/gadget","pullRequest":{"id":"PR_b","number":9}},
		  "p2":{"nameWithOwner":"other/thing","pullRequest":{"id":"PR_c","number":1}}}}`), nil
	}}
	r, gov := newResolver(t, gh)
	refs := []Ref{mustRef(t, "acme-org/WIDGET#3"), mustRef(t, "acme-org/gadget#9"), mustRef(t, "other/thing#1")}
	got, cost, err := r.Resolve(context.Background(), interactive, refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.calls) != 1 {
		t.Fatalf("%d gh calls for three first sightings, want exactly 1", len(gh.calls))
	}
	if cost != 1 {
		t.Errorf("cost = %d, want the document's own rateLimit cost 1", cost)
	}
	args := gh.calls[0]
	doc, _ := argValue(args, "query")
	for i := range refs {
		for _, want := range []string{fmt.Sprintf("p%d: repository(owner: $o%d, name: $r%d)", i, i, i), fmt.Sprintf("pullRequest(number: $n%d)", i)} {
			if !strings.Contains(doc, want) {
				t.Errorf("document lacks %q:\n%s", want, doc)
			}
		}
	}
	if v, _ := argValue(args, "o0"); v != "acme-org" {
		t.Errorf("o0 = %q", v)
	}
	if v, _ := argValue(args, "n1"); v != "9" {
		t.Errorf("n1 = %q", v)
	}
	want := []struct{ node, ref string }{{"PR_a", "Acme-Org/Widget#3"}, {"PR_b", "acme-org/gadget#9"}, {"PR_c", "other/thing#1"}}
	for i, w := range want {
		if !got[i].Found || got[i].NodeID != w.node || got[i].Ref.String() != w.ref || got[i].FromStore || got[i].Renamed {
			t.Errorf("resolution %d = %+v, want %s %s", i, got[i], w.node, w.ref)
		}
	}
	if bs, _ := gov.State().Bucket(governor.BucketGraphQL); bs.Spent != 1 || bs.Remaining != 4900 {
		t.Errorf("governor saw spent %d remaining %d, want 1 and 4900", bs.Spent, bs.Remaining)
	}

	// The aliases are now stored: asking again, in other case, makes no call.
	again, _, err := r.Resolve(context.Background(), interactive, []Ref{mustRef(t, "ACME-ORG/widget#3")})
	if err != nil || len(gh.calls) != 1 {
		t.Fatalf("second resolve: err %v, %d calls", err, len(gh.calls))
	}
	if !again[0].FromStore || again[0].NodeID != "PR_a" || again[0].Ref.String() != "Acme-Org/Widget#3" {
		t.Errorf("stored resolution = %+v", again[0])
	}
}

func TestResolve_SplitsLargeSetsAndDedupes(t *testing.T) {
	gh := &countingGH{answer: func(args []string) ([]byte, error) {
		var fields []string
		for i := 0; ; i++ {
			n, ok := argValue(args, fmt.Sprintf("n%d", i))
			if !ok {
				break
			}
			o, _ := argValue(args, fmt.Sprintf("o%d", i))
			rp, _ := argValue(args, fmt.Sprintf("r%d", i))
			fields = append(fields, fmt.Sprintf(`"p%d":{"nameWithOwner":"%s/%s","pullRequest":{"id":"PR_%s_%s_%s","number":%s}}`, i, o, rp, o, rp, n, n))
		}
		return []byte(`{"data":{` + rl + `,` + strings.Join(fields, ",") + `}}`), nil
	}}
	r, _ := newResolver(t, gh)
	r.BatchSize = 2
	refs := []Ref{
		mustRef(t, "o/r#1"), mustRef(t, "o/r#2"), mustRef(t, "o/r#3"), mustRef(t, "O/R#1"), mustRef(t, "o/r#4"),
	}
	got, _, err := r.Resolve(context.Background(), interactive, refs)
	if err != nil {
		t.Fatal(err)
	}
	// Four distinct references, two per document.
	if len(gh.calls) != 2 {
		t.Fatalf("%d calls for 4 distinct references at batch size 2, want 2", len(gh.calls))
	}
	for i, c := range gh.calls {
		if _, ok := argValue(c, "n2"); ok {
			t.Errorf("call %d carries more than 2 references", i)
		}
	}
	if got[0].NodeID != "PR_o_r_1" || got[3].NodeID != "PR_o_r_1" || got[4].NodeID != "PR_o_r_4" {
		t.Errorf("resolutions = %+v", got)
	}
	if got[3].Requested.String() != "O/R#1" {
		t.Errorf("the duplicate keeps the caller's own spelling, got %v", got[3].Requested)
	}
}

func TestResolve_RenameKeepsNodeIDAndTellsTheCaller(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) {
		return []byte(`{"data":{` + rl + `,"p0":{"nameWithOwner":"acme/gadget","pullRequest":{"id":"PR_1","number":7}}}}`), nil
	}}
	r, _ := newResolver(t, gh)
	ctx := context.Background()
	if _, err := r.Identity.Put(ctx, "PR_1", mustRef(t, "acme/widget#7")); err != nil {
		t.Fatal(err)
	}
	// The caller asks by the NEW name, which the table does not know; GitHub
	// answers with the node id the table already holds under the old name.
	got, _, err := r.Resolve(ctx, interactive, []Ref{mustRef(t, "acme/gadget#7")})
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Found || got[0].NodeID != "PR_1" || !got[0].Renamed || got[0].OldRef.String() != "acme/widget#7" {
		t.Errorf("resolution = %+v, want PR_1 renamed from acme/widget#7", got[0])
	}
	if ref, ok, _ := r.Identity.Ref(ctx, "PR_1"); !ok || ref.String() != "acme/gadget#7" {
		t.Errorf("alias after rename = %v ok=%v", ref, ok)
	}
}

func TestResolve_MissingPullRequestIsNotAnError(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) {
		return []byte(`{"data":{` + rl + `,"p0":{"nameWithOwner":"acme/widget","pullRequest":null},"p1":null,
		  "p2":{"nameWithOwner":"acme/widget","pullRequest":{"id":"PR_2","number":2}}},
		  "errors":[{"type":"NOT_FOUND","path":["p0","pullRequest"]},{"type":"NOT_FOUND","path":["p1"]}]}`),
			errors.New("gh api graphql: exit status 1")
	}}
	r, _ := newResolver(t, gh)
	got, _, err := r.Resolve(context.Background(), interactive,
		[]Ref{mustRef(t, "acme/widget#99"), mustRef(t, "gone/repo#1"), mustRef(t, "acme/widget#2")})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Found || got[1].Found || !got[2].Found || got[2].NodeID != "PR_2" {
		t.Errorf("resolutions = %+v", got)
	}
	if _, _, ok, _ := r.Identity.NodeID(context.Background(), mustRef(t, "acme/widget#99")); ok {
		t.Error("a missing PR must not leave an alias")
	}
}

func TestResolve_OtherFailuresAreErrors(t *testing.T) {
	boom := errors.New("gh api graphql: exit status 1")
	for name, answer := range map[string]func([]string) ([]byte, error){
		"gh failed, no body": func([]string) ([]byte, error) { return nil, boom },
		"graphql error": func([]string) ([]byte, error) {
			return []byte(`{"data":{"p0":null},"errors":[{"type":"FORBIDDEN"}]}`), boom
		},
		"unparsable, gh ok": func([]string) ([]byte, error) { return []byte(`not json`), nil },
		"bad reference answer": func([]string) ([]byte, error) {
			return []byte(`{"data":{"p0":{"nameWithOwner":"x","pullRequest":{"id":"PR_1","number":1}}}}`), nil
		},
	} {
		r, _ := newResolver(t, &countingGH{answer: answer})
		if _, _, err := r.Resolve(context.Background(), interactive, []Ref{mustRef(t, "o/r#1")}); err == nil {
			t.Errorf("%s: Resolve returned no error", name)
		}
	}
}

func TestResolve_GovernorRefusalMakesNoCall(t *testing.T) {
	gh := &countingGH{answer: func([]string) ([]byte, error) { return nil, errors.New("must not run") }}
	r, gov := newResolver(t, gh)
	gov.PauseAll(60e9, "test pause")
	_, _, err := r.Resolve(context.Background(), ratefeed.Meta{Class: governor.ClassFill}, []Ref{mustRef(t, "o/r#1")})
	if !errors.Is(err, governor.ErrRefused) || len(gh.calls) != 0 {
		t.Errorf("err = %v, %d calls; want a refusal and no call", err, len(gh.calls))
	}
}
