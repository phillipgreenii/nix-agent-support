package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/backfill"
)

// markRig is a fake connector and a fake pg-desk, two shell scripts over one state
// directory: the connector answers the tracker listing from anchors.json, marks a PR
// watched when it is read with --watch (unless the PR is listed in nowatch) and counts
// the watched PRs of an inspection; pg-desk records the refreshes.
func markRig(t *testing.T, anchors string, nowatch ...string) (c *cli, dir string) {
	t.Helper()
	dir = t.TempDir()
	write := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("anchors.json", anchors, 0o600)
	write("nowatch", strings.Join(nowatch, "\n")+"\n", 0o600)
	write("watched", "", 0o600)
	write("refreshed", "", 0o600)
	write("queries", "mine\n", 0o600)
	connector := write("connector", `#!/bin/sh
D='`+dir+`'
case "$1 $2" in
"issue list") cat "$D/anchors.json";;
"pr list") echo '{"present_ids":[],"sources":[{"source":"s","status":"succeeded"}],"truncated":false}';;
"pr show")
  grep -qxF "$3" "$D/nowatch" || echo "$3" >> "$D/watched"
  echo '{"protocolVersion":1,"result":{}}';;
"pr inspect")
  shift 2; r=0; n=0
  for a; do
    case "$a" in --*) break;; esac
    r=$((r + 1)); grep -qxF "$a" "$D/watched" && n=$((n + 1))
  done
  printf '{"protocolVersion":1,"result":{"counts":{"requested":%s,"watched":%s}}}\n' "$r" "$n";;
*) echo "unexpected: $*" >&2; exit 2;;
esac
`, 0o755)
	desk := write("desk", "#!/bin/sh\necho \"$3\" >> '"+dir+"/refreshed'\n", 0o755)
	c = newCLI(t, map[string]any{
		"queries_file": filepath.Join(dir, "queries"), "connector": connector, "desk_cmd": desk,
		"rate_max_age": "876000h", "backend": "tracker-x", "beads_dir": dir,
	})
	// newCLI points rate_log at its own directory.
	if err := os.WriteFile(filepath.Join(c.dir, "events.jsonl"), []byte(`{"time":"2026-10-24T10:00:00Z","graphql_remaining":9000}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return c, dir
}

const markAnchors = `{"entities":[
 {"id":"bd-1","title":"o/r#1: one","state":"open","issue_type":"merge-request","metadata":{"state":"open"}},
 {"id":"bd-2","title":"o/r#2: two","state":"open","issue_type":"merge-request","metadata":{"state":"open"}},
 {"id":"bd-3","title":"o/r#3: three","state":"open","issue_type":"merge-request","metadata":{"state":"merged"}},
 {"id":"bd-4","title":"a title naming no PR","state":"open","issue_type":"merge-request"}],
 "sources":[{"source":"s","status":"succeeded"}],"truncated":false}`

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func TestMarkWatchedMarksTheOpenAnchorPRsAndChecksTheCount(t *testing.T) {
	c, dir := markRig(t, markAnchors)
	code, out, errb := c.run("mark-watched", "--pace", "0s")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	for _, want := range []string{"UNMAPPED anchor bd-4", "skip o/r#3", "ok o/r#1 (marked watched)", "ok o/r#2 (refreshed)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	last := lastLine(out)
	if !strings.HasPrefix(last, "DONE mode=mark-watched ") || !strings.Contains(last, "watched=2 expected=2") || !strings.Contains(last, "unmapped=1") {
		t.Fatalf("last line: %s", last)
	}
	w, _ := os.ReadFile(filepath.Join(dir, "watched"))
	r, _ := os.ReadFile(filepath.Join(dir, "refreshed"))
	if string(w) != "o/r#1\no/r#2\n" || string(r) != "o/r#1\no/r#2\n" {
		t.Errorf("watched %q refreshed %q (a merged-PR anchor must not be marked)", w, r)
	}
	// The persisted set is the enumerate command's: the marking step leaves it alone.
	if _, err := os.Stat(filepath.Join(c.dir, "set.json")); err == nil {
		t.Error("mark-watched wrote the persisted set")
	}
	// --from-set marks from the persisted set instead of enumerating.
	if code, _, _ := c.run("enumerate"); code != 0 {
		t.Fatalf("enumerate: %d", code)
	}
	if code, out, errb := c.run("mark-watched", "--from-set", "--pace", "0s"); code != 0 {
		t.Fatalf("from-set: %d\n%s\n%s", code, out, errb)
	}
}

func TestMarkWatchedAWatchCountMismatchExitsNonZero(t *testing.T) {
	c, _ := markRig(t, markAnchors, "o/r#2") // the daemon double does not watch #2
	code, out, _ := c.run("mark-watched", "--pace", "0s")
	last := lastLine(out)
	if code != backfill.ExitSummaryFailed || !strings.HasPrefix(last, "FAILED mode=mark-watched ") || !strings.Contains(last, "watched=1 expected=2") {
		t.Fatalf("exit %d: %s", code, last)
	}
}

func TestMarkWatchedFailsClosedOnAnEmptyAnchorListing(t *testing.T) {
	c, _ := markRig(t, `{"entities":[],"sources":[{"source":"s","status":"succeeded"}],"truncated":false}`)
	code, _, errb := c.run("mark-watched", "--pace", "0s")
	if code != exitEnumerateFailed || !strings.Contains(errb, "enumeration FAILED") {
		t.Fatalf("exit %d: %s", code, errb)
	}
}
