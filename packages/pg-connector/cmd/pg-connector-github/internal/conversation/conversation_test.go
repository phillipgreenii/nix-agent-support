package conversation

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func TestDecode_RoundTripsTheStoredContent(t *testing.T) {
	empty := ""
	c := Content{
		Comments: []schema.PRComment{{ID: "IC_1", Author: "teammate", Body: "hi"}},
		Reviews: []schema.PRReview{{
			ID: "PRR_1", Author: "teammate", State: "APPROVED", CommitOID: &empty,
			Comments: []schema.PRComment{{ID: "RC_1", ReviewThreadID: "PRRT_1", Resolved: true, ThreadOutdated: true}},
		}},
		Connections: schema.PRConnections{Reviews: schema.PRConnectionReport{Total: 1, Returned: 1}},
	}
	b, err := c.canonical()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 1 || got.Comments[0].ID != "IC_1" || len(got.Reviews) != 1 || got.Reviews[0].CommitOID == nil ||
		*got.Reviews[0].CommitOID != "" || got.Reviews[0].Comments[0].ReviewThreadID != "PRRT_1" || got.Connections != c.Connections {
		t.Errorf("Decode = %+v, want %+v", got, c)
	}
}

func TestDecode_RejectsNoContentAndGarbage(t *testing.T) {
	for name, in := range map[string][]byte{"nil": nil, "empty": {}, "garbage": []byte("nope")} {
		if _, err := Decode(in); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestCanonical_AnEmptyConversationIsTwoEmptyArrays(t *testing.T) {
	b, err := Content{}.canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"comments":[],"reviews":[],"connections":{"reviews":{"truncated":false,"total":0,"returned":0},` +
		`"threads":{"truncated":false,"total":0,"returned":0},"comments":{"truncated":false,"total":0,"returned":0}}}`
	if string(b) != want {
		t.Errorf("canonical = %s, want %s", b, want)
	}
}

func TestFingerprint_HashesTheWholeContent(t *testing.T) {
	base := Content{Comments: []schema.PRComment{{ID: "IC_1", Body: "a"}}}
	f1, err := Fingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	same, _ := Fingerprint(Content{Comments: []schema.PRComment{{ID: "IC_1", Body: "a"}}})
	edited, _ := Fingerprint(Content{Comments: []schema.PRComment{{ID: "IC_1", Body: "b"}}})
	resolved, _ := Fingerprint(Content{Comments: []schema.PRComment{{ID: "IC_1", Body: "a", Resolved: true}}})
	truncated, _ := Fingerprint(Content{Comments: base.Comments, Connections: schema.PRConnections{Threads: schema.PRConnectionReport{Truncated: true}}})
	if f1 != same || f1 == edited || f1 == resolved || f1 == truncated || len(f1) != 64 || strings.Trim(f1, "0123456789abcdef") != "" {
		t.Errorf("fingerprints: base %s same %s edited %s resolved %s truncated %s", f1, same, edited, resolved, truncated)
	}
}
