package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// pinnedEmitTime is the fixed adapter emit instant the retry-window tests
// install as changesNow.
var pinnedEmitTime = time.Date(2026, 10, 7, 12, 0, 0, 123456789, time.UTC)

func pinEmitTime(t *testing.T) {
	t.Helper()
	orig := changesNow
	changesNow = func() time.Time { return pinnedEmitTime }
	t.Cleanup(func() { changesNow = orig })
}

// suffixedID matches <entity id>@<12 lowercase hex>.
var suffixedID = regexp.MustCompile(`^(.+)@([0-9a-f]{12})$`)

// onePRItems runs changes --retry-window against the changes_pr_one double with
// the given entity-field environment and returns the single emitted item.
func onePRItems(t *testing.T, window string, env ...string) rawItem {
	t.Helper()
	withFactory(t, "changes_pr_one", env...)
	args := []string{"changes", "pr", "q", "--consumer", "c1"}
	if window != "" {
		args = append(args, "--retry-window", window)
	}
	stdout, stderr, code := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1 (stdout=%q)", len(items), stdout)
	}
	return items[0]
}

// metadataStrings reads a []any-typed JSON array field back out of a
// decoded rawItem's Metadata map as []string, for assertions.
func metadataStrings(t *testing.T, item rawItem, key string) []string {
	t.Helper()
	raw, ok := item.Metadata[key]
	if !ok {
		t.Fatalf("metadata[%q] missing (metadata=%v)", key, item.Metadata)
	}
	vals, ok := raw.([]any)
	if !ok {
		t.Fatalf("metadata[%q] = %T, want []any", key, raw)
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("metadata[%q] element = %T, want string", key, v)
		}
		out = append(out, s)
	}
	return out
}

func TestChanges_DegradedOutcome_ExitsZeroWithDegradedSourcesNamed(t *testing.T) {
	withFactory(t, "changes_degraded")
	stdout, stderr, code := runCLI(t, "changes", "issue", "feedback-ready", "--consumer", "c1")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (items=%+v)", len(items), items)
	}

	// Item 0: non-empty title kept as-is; degraded_sources names only
	// the degraded backend (b2), never the succeeded (b1) or disabled
	// (b3) ones.
	got := items[0]
	if got.ID != "e1" || got.Type != "issue" || got.Title != "Entity One" {
		t.Fatalf("items[0] = %+v", got)
	}
	if got.Metadata["change"] != "added" || got.Metadata["source"] != "b1" {
		t.Fatalf("items[0].metadata = %v", got.Metadata)
	}
	if ds := metadataStrings(t, got, "degraded_sources"); len(ds) != 1 || ds[0] != "b2" {
		t.Fatalf("items[0].metadata.degraded_sources = %v, want [b2]", ds)
	}

	// Item 1: empty entity title falls back to the entity's own id;
	// SAME degraded_sources list as item 0 (call-level, not per-entity).
	got = items[1]
	if got.ID != "e2" || got.Title != "e2" {
		t.Fatalf("items[1] = %+v, want title fallback to id", got)
	}
	if ds := metadataStrings(t, got, "degraded_sources"); len(ds) != 1 || ds[0] != "b2" {
		t.Fatalf("items[1].metadata.degraded_sources = %v, want [b2]", ds)
	}
}

// bead pg2-wa5uk: the partial-degraded (exit 2) path now also forwards
// each degraded backend's own real reason into degraded_reasons,
// alongside the pre-existing degraded_sources name list.
func TestChanges_DegradedOutcomeWithReason_ForwardsReasonInMetadata(t *testing.T) {
	withFactory(t, "changes_degraded_with_reason")
	stdout, stderr, code := runCLI(t, "changes", "issue", "feedback-ready", "--consumer", "c1")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1 (items=%+v)", len(items), items)
	}

	got := items[0]
	if ds := metadataStrings(t, got, "degraded_sources"); len(ds) != 1 || ds[0] != "b2" {
		t.Fatalf("items[0].metadata.degraded_sources = %v, want [b2]", ds)
	}
	reasons, ok := got.Metadata["degraded_reasons"].(map[string]any)
	if !ok {
		t.Fatalf("items[0].metadata.degraded_reasons = %T, want map[string]any (metadata=%v)", got.Metadata["degraded_reasons"], got.Metadata)
	}
	if reasons["b2"] != "rate limited: too many requests" {
		t.Fatalf("items[0].metadata.degraded_reasons = %v, want b2's real reason", reasons)
	}
}

// The pre-existing "changes_degraded" fixture carries no reason on its
// degraded row (omitempty), so degraded_reasons must be absent entirely
// rather than present-but-empty — no behavior change for a call that
// never had a reason to forward.
func TestChanges_DegradedOutcomeWithoutReason_OmitsDegradedReasonsKey(t *testing.T) {
	withFactory(t, "changes_degraded")
	stdout, stderr, code := runCLI(t, "changes", "issue", "feedback-ready", "--consumer", "c1")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) == 0 {
		t.Fatal("want at least one item")
	}
	if _, present := items[0].Metadata["degraded_reasons"]; present {
		t.Fatalf("items[0].metadata.degraded_reasons = %v, want key absent when no degraded source carried a reason", items[0].Metadata["degraded_reasons"])
	}
}

// bead pg2-wa5uk: the exact observed real-world scenario — a single
// degraded (rate-limited) backend, no other healthy source, pg-connector's
// own stderr always empty for this outcome by design. This adapter's own
// stderr must now carry the real reason decoded from pg-connector's own
// stdout, not be silent.
func TestChanges_TotalFailureWithReason_ForwardsReasonToStderr(t *testing.T) {
	withFactory(t, "total_failure_with_reason")
	stdout, stderr, code := runCLI(t, "changes", "issue", "feedback-ready", "--consumer", "c1")

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "b1: rate limited: too many requests") {
		t.Fatalf("stderr = %q, want pg-connector's own real reason (decoded from its stdout) forwarded", stderr)
	}
}

func TestChanges_TotalFailure_ExitsNonZeroPrintingNothingOnStdout(t *testing.T) {
	withFactory(t, "total_failure")
	stdout, stderr, code := runCLI(t, "changes", "issue", "feedback-ready", "--consumer", "c1")

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty (this adapter MUST NOT propagate pg-connector's own exit 3 stdout body)", stdout)
	}
	if !strings.Contains(stderr, "total failure: no backend succeeded") {
		t.Fatalf("stderr = %q, want pg-connector's own stderr copied through", stderr)
	}
	// pg-connector's own exit code (3) must never surface as this
	// adapter's own exit code.
	if code == 3 {
		t.Fatalf("exit code = 3, this adapter MUST NOT propagate pg-connector's own exit code as its own")
	}
}

func TestChanges_InvalidArgument_ExitsNonZeroPrintingNothingOnStdout(t *testing.T) {
	withFactory(t, "invalid_argument")
	stdout, stderr, code := runCLI(t, "changes", "issue", "bogus", "--consumer", "c1")

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "invalid_argument") {
		t.Fatalf("stderr = %q, want pg-connector's own stderr copied through", stderr)
	}
}

func TestChanges_BeadsDirSetsPgConnectorIssueBeadsDirInChildEnv(t *testing.T) {
	recordFile := filepath.Join(t.TempDir(), "recorded-env")
	withFactory(
		t, "changes_ok_empty",
		"GO_HELPER_ENV_RECORD_FILE="+recordFile,
		"GO_HELPER_ENV_RECORD_VAR=PG_CONNECTOR_ISSUE_BEADS_DIR",
	)

	_, stderr, code := runCLI(t, "changes", "issue", "q", "--consumer", "c1", "--beads-dir", "/some/beads/path")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}

	got := readFile(t, recordFile)
	if got != "/some/beads/path" {
		t.Fatalf("child's own PG_CONNECTOR_ISSUE_BEADS_DIR = %q, want /some/beads/path", got)
	}
}

func TestChanges_NoBeadsDirFlag_LeavesPgConnectorIssueBeadsDirUnset(t *testing.T) {
	recordFile := filepath.Join(t.TempDir(), "recorded-env")
	withFactory(
		t, "changes_ok_empty",
		"GO_HELPER_ENV_RECORD_FILE="+recordFile,
		"GO_HELPER_ENV_RECORD_VAR=PG_CONNECTOR_ISSUE_BEADS_DIR",
	)

	_, stderr, code := runCLI(t, "changes", "issue", "q", "--consumer", "c1")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}

	got := readFile(t, recordFile)
	if got != "<unset>" {
		t.Fatalf("child's own PG_CONNECTOR_ISSUE_BEADS_DIR = %q, want left unset entirely", got)
	}
}

// bead pg2-otfq2: --backend is an optional pass-through. Omitted, the argv
// carries no --backend (fan-out over every backend, as before); given, it is
// forwarded verbatim.
func TestChanges_BackendFlag_IsPassedThroughOnlyWhenGiven(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string // the --backend value in the child's argv; "" means absent
	}{
		{"omitted", []string{"changes", "issue", "q", "--consumer", "c1"}, ""},
		{"given", []string{"changes", "issue", "q", "--consumer", "c1", "--backend", "pg-connector-issue-beads-zr"}, "pg-connector-issue-beads-zr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recordFile := filepath.Join(t.TempDir(), "recorded-args")
			withFactory(t, "changes_ok_empty", "GO_HELPER_ARGS_RECORD_FILE="+recordFile)
			if _, stderr, code := runCLI(t, tc.args...); code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
			}
			got := strings.Fields(readFile(t, recordFile))
			if v := flagValue(got, "--backend"); v != tc.want {
				t.Fatalf("child argv %v: --backend = %q, want %q", got, v, tc.want)
			}
		})
	}
}

// --- bead pg2-1ldvy: metadata.entity_id, --retry-window ----------------------

// entity_id is ALWAYS emitted on a changes item, with or without the
// retry window, carrying the bare entity id.
func TestChanges_EntityIDAlwaysPresent(t *testing.T) {
	pinEmitTime(t)
	for _, window := range []string{"", "0", "30m"} {
		t.Run("window="+window, func(t *testing.T) {
			withFactory(t, "changes_degraded")
			args := []string{"changes", "issue", "q", "--consumer", "c1"}
			if window != "" {
				args = append(args, "--retry-window", window)
			}
			stdout, stderr, code := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
			}
			items := mustUnmarshalItems(t, stdout)
			if len(items) != 2 {
				t.Fatalf("len(items) = %d, want 2", len(items))
			}
			for i, want := range []string{"e1", "e2"} {
				if got := items[i].Metadata["entity_id"]; got != want {
					t.Fatalf("items[%d].metadata.entity_id = %v, want bare %q", i, got, want)
				}
			}
		})
	}
}

// Window 0 / absent: the only change versus before the bead is
// metadata.entity_id. The exact bytes are pinned so any other drift (an id
// suffix, an at/expiresAt key) fails.
func TestChanges_WindowZeroOrAbsent_IsByteIdenticalApartFromEntityID(t *testing.T) {
	pinEmitTime(t)
	const want = `[{"id":"e1","type":"issue","title":"Entity One","metadata":{"change":"added","degraded_sources":["b2"],"entity_id":"e1","source":"b1"}},` +
		`{"id":"e2","type":"issue","title":"e2","metadata":{"change":"changed","degraded_sources":["b2"],"entity_id":"e2","source":"b2"}}]` + "\n"
	for _, extra := range [][]string{nil, {"--retry-window", "0"}, {"--retry-window", "0s"}} {
		withFactory(t, "changes_degraded")
		args := append([]string{"changes", "issue", "q", "--consumer", "c1"}, extra...)
		stdout, stderr, code := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit code = %d, want 0 (stderr=%q)", extra, code, stderr)
		}
		if stdout != want {
			t.Fatalf("%v: stdout =\n%s\nwant\n%s", extra, stdout, want)
		}
		if strings.Contains(stdout, `"at"`) || strings.Contains(stdout, "expiresAt") {
			t.Fatalf("%v: window 0 must emit no at/expiresAt, got %s", extra, stdout)
		}
	}
}

func TestChanges_RetryWindow_SuffixesIDAndStampsAtAndExpiresAt(t *testing.T) {
	pinEmitTime(t)
	withFactory(t, "changes_pr_rows")
	stdout, stderr, code := runCLI(t, "changes", "pr", "q", "--consumer", "c1", "--retry-window", "90m")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 5 {
		t.Fatalf("len(items) = %d, want 5", len(items))
	}
	wantAt := pinnedEmitTime.Truncate(time.Second).Format(time.RFC3339)
	wantExpires := pinnedEmitTime.Truncate(time.Second).Add(90 * time.Minute).Format(time.RFC3339)
	if wantAt != "2026-10-07T12:00:00Z" || wantExpires != "2026-10-07T13:30:00Z" {
		t.Fatalf("test arithmetic: at=%s expires=%s", wantAt, wantExpires)
	}
	for i, it := range items {
		m := suffixedID.FindStringSubmatch(it.ID)
		if m == nil {
			t.Fatalf("items[%d].id = %q, want <entity id>@<12 hex>", i, it.ID)
		}
		if bare := it.Metadata["entity_id"]; bare != m[1] {
			t.Fatalf("items[%d]: metadata.entity_id = %v, want the bare id %q", i, bare, m[1])
		}
		if it.At != wantAt || it.ExpiresAt != wantExpires {
			t.Fatalf("items[%d]: at=%q expiresAt=%q, want %q / %q", i, it.At, it.ExpiresAt, wantAt, wantExpires)
		}
		// expiresAt = at + window, parsed back.
		at, err1 := time.Parse(time.RFC3339, it.At)
		exp, err2 := time.Parse(time.RFC3339, it.ExpiresAt)
		if err1 != nil || err2 != nil || exp.Sub(at) != 90*time.Minute {
			t.Fatalf("items[%d]: expiresAt-at = %v (errs %v %v), want 90m", i, exp.Sub(at), err1, err2)
		}
	}
}

// Distinct rows (change kind, source, entity id, head_sha) get distinct ids;
// an identical row gets the same id on a repeat invocation.
func TestChanges_RetryWindow_DistinctRowsDistinctIDsIdenticalRowSameID(t *testing.T) {
	pinEmitTime(t)
	run := func() []rawItem {
		withFactory(t, "changes_pr_rows")
		stdout, stderr, code := runCLI(t, "changes", "pr", "q", "--consumer", "c1", "--retry-window", "1h")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
		}
		return mustUnmarshalItems(t, stdout)
	}
	first := run()
	seen := map[string]int{}
	for i, it := range first {
		if j, dup := seen[it.ID]; dup {
			t.Fatalf("items[%d] and items[%d] share id %q, want every distinct row distinct", j, i, it.ID)
		}
		seen[it.ID] = i
	}
	second := run()
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("items[%d]: id %q then %q across identical re-reports, want identical", i, first[i].ID, second[i].ID)
		}
	}
}

// Hash stability: the volatile fields (as_of, stale) and every pure content
// field (title, comment_count) MUST NOT reach the digest, so a re-reported
// identical row dedupes; the stable fields (head_sha, version) MUST.
func TestChanges_RetryWindow_DigestIgnoresVolatileAndContentFields(t *testing.T) {
	pinEmitTime(t)
	base := onePRItems(t, "1h", "GO_HELPER_HEAD=h1", "GO_HELPER_ASOF=2026-10-07T10:00:00Z", "GO_HELPER_TITLE=a")
	for name, env := range map[string][]string{
		"as_of moved": {"GO_HELPER_HEAD=h1", "GO_HELPER_ASOF=2026-10-07T11:59:59Z", "GO_HELPER_TITLE=a"},
		"stale flip":  {"GO_HELPER_HEAD=h1", "GO_HELPER_ASOF=2026-10-07T10:00:00Z", "GO_HELPER_TITLE=a", "GO_HELPER_STALE=1"},
		"title/count": {"GO_HELPER_HEAD=h1", "GO_HELPER_ASOF=2026-10-07T10:00:00Z", "GO_HELPER_TITLE=a much longer title"},
	} {
		if got := onePRItems(t, "1h", env...); got.ID != base.ID {
			t.Errorf("%s: id %q != base %q, a volatile/content field reached the digest", name, got.ID, base.ID)
		}
	}
	if got := onePRItems(t, "1h", "GO_HELPER_HEAD=h2", "GO_HELPER_ASOF=2026-10-07T10:00:00Z", "GO_HELPER_TITLE=a"); got.ID == base.ID {
		t.Errorf("head_sha h2 produced the same id %q as h1", got.ID)
	}
	v1 := onePRItems(t, "1h", "GO_HELPER_VERSION=7")
	v2 := onePRItems(t, "1h", "GO_HELPER_VERSION=8")
	if v1.ID == v2.ID {
		t.Errorf("version 7 and 8 share id %q, want version in the digest", v1.ID)
	}
	if v1.ID == base.ID {
		t.Errorf("a version-only entity shares id %q with a head_sha entity", v1.ID)
	}
}

// DECISION (bead pg2-1ldvy item 3, recorded in the docs): an A->B->A state
// flip inside one window hashes the third report to the same id as the first,
// so the queue dedupes it while the first is retained — a rare lost update the
// ~30m sweep covers. Pinned here so changing it is a deliberate act.
func TestChanges_RetryWindow_ABAFlipHashesToTheFirstID(t *testing.T) {
	pinEmitTime(t)
	a1 := onePRItems(t, "1h", "GO_HELPER_HEAD=A")
	b := onePRItems(t, "1h", "GO_HELPER_HEAD=B")
	a2 := onePRItems(t, "1h", "GO_HELPER_HEAD=A")
	if a1.ID == b.ID {
		t.Fatalf("A and B share id %q", a1.ID)
	}
	if a1.ID != a2.ID {
		t.Fatalf("A->B->A: third id %q != first id %q (documented lost-update trade-off)", a2.ID, a1.ID)
	}
}

// The digest is pinned to the canonical-JSON recipe so a refactor cannot
// silently change every id the router has retained.
func TestChanges_RetryWindow_DigestRecipeIsPinned(t *testing.T) {
	id := entityIdentity{ID: "o/r#1", HeadSHA: []byte(` "h1" `)}
	got, err := changeDigest(changesEntry{Change: "changed", Source: "gh"}, id)
	if err != nil {
		t.Fatal(err)
	}
	// sha256 of {"change":"changed","source":"gh","entity_id":"o/r#1","head_sha":"h1"}
	if want := "c42b5d1c27eb"; got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
}

// ownedRow builds one change row of a backend that owns its changes, over the
// generic PR o/r#1, with the given own version, head and extras.
func ownedRow(version int, head, extras string) string {
	return fmt.Sprintf(`{"change":"changed","source":"gh","entity":{"id":"o/r#1","title":"One","head_sha":%q,"version":%d},%s}`,
		head, version, extras)
}

// pollBody wraps rows in the extended answer: sources carry next_seq and the
// answer carries sources_freshness.
func pollBody(rows ...string) string {
	return `{"sources":[{"backend":"gh","status":"succeeded","version":3,"truncated":false,"next_seq":9}],` +
		`"changes":[` + strings.Join(rows, ",") + `],` +
		`"sources_freshness":[{"query":"q","last_success_at":"2026-10-07T11:00:00Z"}]}`
}

// poll runs one changes invocation against the given canned answer.
func poll(t *testing.T, window, body string) (stdout string, items []rawItem) {
	t.Helper()
	pinEmitTime(t)
	withFactory(t, "changes_body_env", "GO_HELPER_BODY="+body)
	args := []string{"changes", "pr", "q", "--consumer", "c1"}
	if window != "" {
		args = append(args, "--retry-window", window)
	}
	stdout, stderr, code := runCLI(t, args...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	return stdout, mustUnmarshalItems(t, stdout)
}

const ownedExtras = `"seq":41,"kinds":["comment","label"],"fields":[{"name":"labels","before":["a"],"after":["a","b"]}]`

// Decision 2: seq, kinds and fields surface on the item's metadata under those
// keys, with fields kept as the raw JSON the backend sent.
func TestChanges_OwnedRow_DecodesSeqKindsFieldsIntoMetadata(t *testing.T) {
	_, items := poll(t, "1h", pollBody(ownedRow(7, "h1", ownedExtras)))
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	m := items[0].Metadata
	if seq, ok := m["seq"].(float64); !ok || seq != 41 {
		t.Fatalf("metadata.seq = %v (%T), want 41", m["seq"], m["seq"])
	}
	if kinds := metadataStrings(t, items[0], "kinds"); len(kinds) != 2 || kinds[0] != "comment" || kinds[1] != "label" {
		t.Fatalf("metadata.kinds = %v, want [comment label]", kinds)
	}
	fields, ok := m["fields"].([]any)
	if !ok || len(fields) != 1 {
		t.Fatalf("metadata.fields = %v (%T), want one element", m["fields"], m["fields"])
	}
	f := fields[0].(map[string]any)
	if f["name"] != "labels" {
		t.Fatalf("metadata.fields[0] = %v", f)
	}
	if !suffixedID.MatchString(items[0].ID) {
		t.Fatalf("id = %q, want <entity id>@<12 hex>", items[0].ID)
	}
	if m["entity_id"] != "o/r#1" {
		t.Fatalf("metadata.entity_id = %v", m["entity_id"])
	}
}

// fields is printed byte-for-byte as sent (compacted), never re-encoded.
func TestChanges_OwnedRow_FieldsPrintedAsRawJSON(t *testing.T) {
	const raw = `[{"name":"title","before":"a <b>","after":1.50}]`
	stdout, _ := poll(t, "", pollBody(ownedRow(7, "h1", `"fields":`+raw)))
	if !strings.Contains(stdout, `"fields":`+raw) {
		t.Fatalf("stdout = %s, want the raw fields %s verbatim", stdout, raw)
	}
}

// Each of the three is independent: only the ones the row carries are printed;
// an absent or null one is nothing.
func TestChanges_OwnedRow_EachExtraIsIndependent(t *testing.T) {
	cases := map[string]struct {
		extras string
		want   []string
	}{
		"seq only":     {`"seq":5`, []string{"seq"}},
		"kinds only":   {`"kinds":["x"]`, []string{"kinds"}},
		"fields only":  {`"fields":[]`, []string{"fields"}},
		"nulls":        {`"seq":null,"kinds":null,"fields":null`, nil},
		"seq + fields": {`"seq":5,"kinds":null,"fields":[]`, []string{"seq", "fields"}},
		"empty kinds":  {`"kinds":[]`, []string{"kinds"}},
	}
	for name, tc := range cases {
		_, items := poll(t, "1h", pollBody(ownedRow(7, "h1", tc.extras)))
		for _, key := range []string{"seq", "kinds", "fields"} {
			_, got := items[0].Metadata[key]
			want := false
			for _, w := range tc.want {
				want = want || w == key
			}
			if got != want {
				t.Errorf("%s: metadata[%q] present = %v, want %v (metadata=%v)", name, key, got, want, items[0].Metadata)
			}
		}
	}
}

// The SAME row (same version and head) delivered in two polls keeps its id even
// when the second answer also carries a newer row for that PR.
func TestChanges_RetryWindow_RedeliveredRowKeepsIDAfterNewerRow(t *testing.T) {
	_, first := poll(t, "1h", pollBody(ownedRow(7, "h1", ownedExtras)))
	_, second := poll(t, "1h", pollBody(
		ownedRow(7, "h1", ownedExtras),
		ownedRow(8, "h1", `"seq":42,"kinds":["review"],"fields":[]`),
	))
	if len(second) != 2 {
		t.Fatalf("len(second) = %d, want 2", len(second))
	}
	if first[0].ID != second[0].ID {
		t.Fatalf("redelivered row id %q then %q, want identical", first[0].ID, second[0].ID)
	}
	if second[0].ID == second[1].ID {
		t.Fatalf("version 7 and 8 rows share id %q", second[0].ID)
	}
}

// Two rows for one PR in one poll, one head, versions 7 and 8, get two ids.
func TestChanges_RetryWindow_TwoVersionsOnOneHeadTwoIDs(t *testing.T) {
	_, items := poll(t, "1h", pollBody(
		ownedRow(7, "h1", ownedExtras),
		ownedRow(8, "h1", ownedExtras),
	))
	if len(items) != 2 || items[0].ID == items[1].ID {
		t.Fatalf("ids = %q, want two distinct ids", []string{items[0].ID, items[1].ID})
	}
	for _, it := range items {
		if !suffixedID.MatchString(it.ID) {
			t.Fatalf("id %q, want <entity id>@<12 hex>", it.ID)
		}
	}
}

// A different seq, kinds or fields with the same version and head is the same
// id: none of the three reaches the digest.
func TestChanges_RetryWindow_SeqKindsFieldsDoNotReachTheDigest(t *testing.T) {
	_, base := poll(t, "1h", pollBody(ownedRow(7, "h1", ownedExtras)))
	for name, extras := range map[string]string{
		"seq":    `"seq":99,"kinds":["comment","label"],"fields":[{"name":"labels","before":["a"],"after":["a","b"]}]`,
		"kinds":  `"seq":41,"kinds":["review"],"fields":[{"name":"labels","before":["a"],"after":["a","b"]}]`,
		"fields": `"seq":41,"kinds":["comment","label"],"fields":[]`,
		"none":   `"seq":null`,
	} {
		_, got := poll(t, "1h", pollBody(ownedRow(7, "h1", extras)))
		if got[0].ID != base[0].ID {
			t.Errorf("%s changed: id %q != base %q, an extra reached the digest", name, got[0].ID, base[0].ID)
		}
	}
	// The digest recipe for an owning backend's row is pinned: {change, source,
	// entity_id, head_sha, version}, version a raw JSON number.
	id := entityIdentity{ID: "o/r#1", HeadSHA: []byte(`"h1"`), Version: []byte(`7`)}
	sum := func(c changesEntry) string {
		d, err := changeDigest(c, id)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	plain := sum(changesEntry{Change: "changed", Source: "gh"})
	withExtras := sum(changesEntry{
		Change: "changed", Source: "gh",
		Seq: []byte(`41`), Kinds: []byte(`["x"]`), Fields: []byte(`[]`),
	})
	if plain != withExtras {
		t.Fatalf("digest %q with extras vs %q without", withExtras, plain)
	}
	want := sha256.Sum256([]byte(`{"change":"changed","source":"gh","entity_id":"o/r#1","head_sha":"h1","version":7}`))
	if plain != hex.EncodeToString(want[:])[:retryIDHexLen] {
		t.Fatalf("digest = %q, want sha256 of the canonical identity", plain)
	}
}

// A row carrying none of the new fields prints exactly as before, with and
// without a window, even inside an answer that carries next_seq and
// sources_freshness.
func TestChanges_RowsWithoutExtras_ByteIdenticalToTodaysOutput(t *testing.T) {
	const body = `{"sources":[{"backend":"b1","status":"succeeded","version":1,"truncated":false}],"changes":[` +
		`{"change":"added","source":"b1","entity":{"id":"e1","title":"Entity One"}}]}`
	const want = `[{"id":"e1","type":"pr","title":"Entity One","metadata":{"change":"added","degraded_sources":[],"entity_id":"e1","source":"b1"}}]` + "\n"
	for _, window := range []string{"", "0", "0s"} {
		if stdout, _ := poll(t, window, body); stdout != want {
			t.Fatalf("window %q: stdout =\n%s\nwant\n%s", window, stdout, want)
		}
	}
	// The extended answer envelope alone (no per-row extras) changes nothing.
	extended := strings.Replace(body, `"truncated":false}`, `"truncated":false,"next_seq":4}`, 1)
	extended = strings.TrimSuffix(extended, "}") + `,"sources_freshness":[]}`
	if stdout, _ := poll(t, "", extended); stdout != want {
		t.Fatalf("extended envelope: stdout =\n%s\nwant\n%s", stdout, want)
	}
	// With a window, a row without extras gains only the id suffix and at/expiresAt.
	stdout, items := poll(t, "1h", body)
	for _, key := range []string{"seq", "kinds", "fields"} {
		if _, ok := items[0].Metadata[key]; ok || strings.Contains(stdout, `"`+key+`"`) {
			t.Fatalf("metadata gained %q for a row without it: %s", key, stdout)
		}
	}
}

// A malformed seq, kinds or fields is a failure that prints nothing on stdout.
func TestChanges_MalformedExtras_FailWithNothingOnStdout(t *testing.T) {
	for name, extras := range map[string]string{
		"seq string":       `"seq":"41"`,
		"seq object":       `"seq":{}`,
		"kinds string":     `"kinds":"comment"`,
		"kinds object":     `"kinds":{}`,
		"kinds number el":  `"kinds":["a",2]`,
		"fields object":    `"fields":{"name":"x"}`,
		"fields string":    `"fields":"x"`,
		"good then broken": `"seq":1,"kinds":["a"],"fields":7`,
	} {
		for _, window := range []string{"", "1h"} {
			pinEmitTime(t)
			withFactory(t, "changes_body_env", "GO_HELPER_BODY="+pollBody(ownedRow(7, "h1", `"seq":1`), ownedRow(8, "h1", extras)))
			args := []string{"changes", "pr", "q", "--consumer", "c1"}
			if window != "" {
				args = append(args, "--retry-window", window)
			}
			stdout, stderr, code := runCLI(t, args...)
			if code == 0 || stdout != "" || stderr == "" {
				t.Errorf("%s (window %q): code=%d stdout=%q stderr=%q, want non-zero, empty stdout, a diagnostic", name, window, code, stdout, stderr)
			}
		}
	}
}

func TestChanges_RetryWindow_NegativeIsUsageError(t *testing.T) {
	stdout, stderr, code := runCLI(t, "changes", "pr", "q", "--consumer", "c1", "--retry-window", "-1m")
	if code == 0 || stdout != "" || !strings.Contains(stderr, "retry-window") {
		t.Fatalf("code=%d stdout=%q stderr=%q, want non-zero, empty stdout, retry-window diagnostic", code, stdout, stderr)
	}
}

func TestChanges_RetryWindow_NonDurationIsUsageError(t *testing.T) {
	stdout, stderr, code := runCLI(t, "changes", "pr", "q", "--consumer", "c1", "--retry-window", "soon")
	if code == 0 || stdout != "" || stderr == "" {
		t.Fatalf("code=%d stdout=%q stderr=%q, want non-zero, empty stdout, a diagnostic", code, stdout, stderr)
	}
}

// Sweep output is untouched by the retry window work: exact bytes.
func TestSweep_OutputByteIdentical(t *testing.T) {
	withFactory(t, "sweep_ids_by_query")
	stdout, stderr, code := runCLI(t, "sweep", "issue", "q1", "q2")
	if code != 0 {
		t.Fatalf("exit code = %d (stderr=%q)", code, stderr)
	}
	const want = `[{"id":"a","type":"issue","title":"a","metadata":{"change":"sweep"}},` +
		`{"id":"b","type":"issue","title":"b","metadata":{"change":"sweep"}},` +
		`{"id":"c","type":"issue","title":"c","metadata":{"change":"sweep"}}]` + "\n"
	if stdout != want {
		t.Fatalf("sweep stdout =\n%s\nwant\n%s", stdout, want)
	}
}
