package parity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// requiredScenarioPrefixes are the twelve cases the packet pins; each scenario
// directory starts with its two-digit number.
var requiredScenarioPrefixes = []string{
	"01-", "02-", "03-", "04-", "05-", "06-", "07-", "08-", "09-", "10-", "11-", "12-",
}

func TestFixtureScenariosCoverRequiredCases(t *testing.T) {
	scs, err := Scenarios()
	if err != nil {
		t.Fatalf("Scenarios: %v", err)
	}
	for _, p := range requiredScenarioPrefixes {
		found := false
		for _, sc := range scs {
			if strings.HasPrefix(sc.Name, p) {
				found = true
			}
		}
		if !found {
			t.Errorf("no scenario starts with %q", p)
		}
	}
	prev := ""
	for _, sc := range scs {
		if sc.Name <= prev {
			t.Errorf("scenarios not sorted by name: %q after %q", sc.Name, prev)
		}
		prev = sc.Name
		if sc.Description == "" {
			t.Errorf("%s: empty description", sc.Name)
		}
		if len(sc.Entities) == 0 {
			t.Errorf("%s: no entities", sc.Name)
		}
		if sc.Dir != "testdata/"+sc.Name {
			t.Errorf("%s: Dir = %q, want testdata/%s", sc.Name, sc.Dir, sc.Name)
		}
		fx, err := LoadFixture(sc)
		if err != nil {
			t.Errorf("%s: LoadFixture: %v", sc.Name, err)
			continue
		}
		if fx.Name != sc.Name {
			t.Errorf("%s: fixture name %q", sc.Name, fx.Name)
		}
	}
}

func TestFixtureIsSyntheticAndSingleRepo(t *testing.T) {
	scs, err := Scenarios()
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range scs {
		for _, e := range sc.Entities {
			if !strings.HasPrefix(e, FixtureRepo+"#") {
				t.Errorf("%s: entity %q is not in the single synthetic repo %s", sc.Name, e, FixtureRepo)
			}
		}
	}
}

func TestFixtureDerivedWireShapes(t *testing.T) {
	fx, err := ParseFixture([]byte(`{
	  "name": "x",
	  "description": "d",
	  "entities": ["acme/api#7"],
	  "prs": [{"number": 7, "author": "teammate", "head_sha": "h7", "mergeable": "CONFLICTING",
	    "comments": [{"id": "c1", "author": "review-bot", "body": "b"}],
	    "commit_authors": ["teammate", "phillipgreenii"],
	    "ci": [{"id": "9", "name": "build", "conclusion": "failure"}]}],
	  "beads": [{"id": "bd-1", "title": "acme/api#7: t", "metadata": {"repo": "acme/api", "pr_number": "7"}, "fbsum": ["c1"]}]
	}`))
	if err != nil {
		t.Fatalf("ParseFixture: %v", err)
	}
	var show struct {
		ID, Repo, Author, Mergeable, State string
		HeadSHA                            string `json:"head_sha"`
		Number                             int
		Comments                           []struct{ ID string }
	}
	mustDecode(t, fx.prShowJSON(fx.PRs[0]), &show)
	if show.ID != "acme/api#7" || show.Repo != FixtureRepo || show.Number != 7 || show.Author != "teammate" ||
		show.HeadSHA != "h7" || show.Mergeable != "CONFLICTING" || show.State != "open" || len(show.Comments) != 1 {
		t.Errorf("pr show = %+v", show)
	}
	var commits struct {
		Commits []struct{ Author string }
	}
	mustDecode(t, fx.prCommitsJSON(fx.PRs[0]), &commits)
	if len(commits.Commits) != 2 || commits.Commits[1].Author != "phillipgreenii" {
		t.Errorf("commits = %+v", commits)
	}
	var ci struct {
		Runs []struct {
			ID, Conclusion, Status string
			HeadSHA                string `json:"head_sha"`
			Attempt                int
		}
	}
	mustDecode(t, fx.ciListJSON(fx.PRs[0]), &ci)
	if len(ci.Runs) != 1 || ci.Runs[0].HeadSHA != "h7" || ci.Runs[0].Status != "completed" || ci.Runs[0].Attempt != 1 {
		t.Errorf("ci = %+v", ci)
	}
	var issue struct {
		ID, State string
		Labels    []string
		Metadata  map[string]string
	}
	mustDecode(t, fx.issueShowJSON(fx.Beads[0]), &issue)
	if issue.ID != "bd-1" || issue.State != "open" || len(issue.Labels) != 1 || !strings.HasPrefix(issue.Labels[0], "fbsum:") {
		t.Errorf("issue = %+v", issue)
	}
	if len(strings.TrimPrefix(issue.Labels[0], "fbsum:")) != 12 {
		t.Errorf("fbsum digest is not 12 hex chars: %q", issue.Labels[0])
	}
	var list struct {
		Entities   []struct{ ID string }
		PresentIDs []string `json:"present_ids"`
	}
	mustDecode(t, fx.workBeadsJSON(), &list)
	if len(list.Entities) != 1 || list.Entities[0].ID != "bd-1" {
		t.Errorf("work-beads = %+v", list)
	}
}

func TestFixtureFbsumMatchesDeciderDigest(t *testing.T) {
	// Pinned against the digest both sides compute: sha256 over each id
	// followed by a NUL byte, hex-encoded, first 12 characters.
	sum := sha256.Sum256([]byte("c1\x00"))
	if got, want := fbsumDigest([]string{"c1"}), hex.EncodeToString(sum[:])[:12]; got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
	if fbsumDigest(nil) != "" {
		t.Fatal("empty set must have an empty digest")
	}
	if fbsumDigest([]string{"a", "b"}) == fbsumDigest([]string{"b", "a"}) {
		t.Fatal("digest must depend on order; the caller sorts")
	}
}

func TestFixtureParseRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"unknown field":      `{"name":"x","description":"d","entities":["acme/api#1"],"prs":[{"number":1}],"bogus":1}`,
		"no entities":        `{"name":"x","description":"d","entities":[],"prs":[{"number":1}]}`,
		"entity without pr":  `{"name":"x","description":"d","entities":["acme/api#2"],"prs":[{"number":1}]}`,
		"foreign repo":       `{"name":"x","description":"d","entities":["other/repo#1"],"prs":[{"number":1}]}`,
		"duplicate pr":       `{"name":"x","description":"d","entities":["acme/api#1"],"prs":[{"number":1},{"number":1}]}`,
		"duplicate bead":     `{"name":"x","description":"d","entities":["acme/api#1"],"prs":[{"number":1}],"beads":[{"id":"b","title":"t"},{"id":"b","title":"t"}]}`,
		"bad bead state":     `{"name":"x","description":"d","entities":["acme/api#1"],"prs":[{"number":1}],"beads":[{"id":"b","title":"t","state":"weird"}]}`,
		"hidden not entity":  `{"name":"x","description":"d","entities":["acme/api#1"],"prs":[{"number":1}],"hidden":["acme/api#9"]}`,
		"duplicate comments": `{"name":"x","description":"d","entities":["acme/api#1"],"prs":[{"number":1,"comments":[{"id":"c"},{"id":"c"}]}]}`,
		"missing name":       `{"description":"d","entities":["acme/api#1"],"prs":[{"number":1}]}`,
	}
	for name, in := range cases {
		if _, err := ParseFixture([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestFixtureLoadRejectsNameDirMismatch(t *testing.T) {
	_, err := LoadFixture(Scenario{Name: "no-such-scenario", Dir: "testdata/no-such-scenario"})
	if err == nil {
		t.Fatal("want an error for a scenario with no fixture file")
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatal("a missing fixture is a bug, not an unsupported scenario")
	}
}

func mustDecode(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}
