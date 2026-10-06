package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func fixture(name string) string { return filepath.Join("testdata", name) }

func readGolden(t *testing.T, name string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	var v []map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var wantMetadataKeys = []string{"degraded_sources", "entity_id", "entity_type", "kinds", "origin", "seq", "version"}

// Golden: envelope fixture -> exact items array (one per entity, in order of
// first record; a record with no kinds still yields its entity, kinds []).
func TestEnvelopeToItemsGolden(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_degraded.json"), "GO_HELPER_EXIT=2")
	stdout, stderr, code := runCLI(t, "widget", "--consumer", "router")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, stderr)
	}
	got := mustItems(t, stdout)
	want := readGolden(t, "items_degraded.golden.json")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items mismatch\n got: %s\nwant: %v", stdout, want)
	}
	for _, it := range got {
		md := it["metadata"].(map[string]any)
		keys := make([]string, 0, len(md))
		for k := range md {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, wantMetadataKeys) {
			t.Errorf("metadata keys = %v, want exactly %v", keys, wantMetadataKeys)
		}
	}
}

// A record whose kinds is empty still yields its entity (one item per
// distinct entity id) with an empty, non-null kinds list.
func TestRecordWithNoKindsYieldsEntityWithEmptyKinds(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_degraded.json"), "GO_HELPER_EXIT=0")
	stdout, _, code := runCLI(t, "widget", "--consumer", "router")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, `"kinds":[]`) {
		t.Errorf("want literal \"kinds\":[] in %q", stdout)
	}
}

// All-ok envelope: degraded_sources is [] (present, not null).
func TestAllOkEnvelopeHasEmptyDegradedSources(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_all_ok.json"), "GO_HELPER_EXIT=0")
	stdout, _, code := runCLI(t, "widget", "--consumer", "router")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, `"degraded_sources":[]`) {
		t.Errorf("want literal \"degraded_sources\":[] in %q", stdout)
	}
	items := mustItems(t, stdout)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	md := items[0]["metadata"].(map[string]any)
	ds, ok := md["degraded_sources"].([]any)
	if !ok || len(ds) != 0 {
		t.Errorf("degraded_sources = %#v, want empty array", md["degraded_sources"])
	}
}

// A failed source counts as degraded detail, in envelope order.
func TestFailedSourceIsListedInDegradedSources(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_failed_source.json"), "GO_HELPER_EXIT=2")
	stdout, _, code := runCLI(t, "widget", "--consumer", "router")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	md := mustItems(t, stdout)[0]["metadata"].(map[string]any)
	if !reflect.DeepEqual(md["degraded_sources"], []any{"open-widgets"}) {
		t.Errorf("degraded_sources = %#v", md["degraded_sources"])
	}
}

// Zero-record envelope: exactly "[]" and exit 0 (never null).
func TestZeroRecordEnvelopeYieldsEmptyArray(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_empty.json"), "GO_HELPER_EXIT=0")
	stdout, _, code := runCLI(t, "widget", "--consumer", "router")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout = %q, want []", stdout)
	}
}

// The operator-ruled translation: pg-desk 2 (partial, cursor advanced) ->
// adapter 0 with every record emitted and degraded_sources set; pg-desk 3
// (nothing logged) -> adapter 1 with empty stdout.
func TestAdapterTranslatesPgDeskExitCodesForPgRouter(t *testing.T) {
	t.Run("pg-desk 2 -> adapter 0, every record, degraded_sources set", func(t *testing.T) {
		withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_degraded.json"), "GO_HELPER_EXIT=2")
		stdout, _, code := runCLI(t, "widget", "--consumer", "router")
		if code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		items := mustItems(t, stdout)
		if len(items) != 3 {
			t.Fatalf("items = %d, want 3 (every entity)", len(items))
		}
		for _, it := range items {
			ds := it["metadata"].(map[string]any)["degraded_sources"].([]any)
			if len(ds) == 0 {
				t.Errorf("degraded_sources empty on %v", it)
			}
		}
	})
	t.Run("pg-desk 3 -> adapter 1, empty stdout, diagnostic on stderr", func(t *testing.T) {
		withHelper(t, "GO_HELPER_STDOUT=ignored-body", "GO_HELPER_STDERR=changes: total failure: nothing delivered\n", "GO_HELPER_EXIT=3")
		stdout, stderr, code := runCLI(t, "widget", "--consumer", "router")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty", stdout)
		}
		if !strings.Contains(stderr, "total failure") {
			t.Errorf("stderr = %q, want pg-desk's diagnostic copied", stderr)
		}
	})
}

func TestUnparseableStdoutFailsClosed(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT=not json at all", "GO_HELPER_EXIT=0")
	stdout, stderr, code := runCLI(t, "widget", "--consumer", "router")
	if code != 1 || stdout != "" || stderr == "" {
		t.Errorf("code=%d stdout=%q stderr=%q; want 1, empty, non-empty", code, stdout, stderr)
	}
}

func TestUnexpectedPgDeskExitCodeFailsClosed(t *testing.T) {
	for _, exit := range []string{"1", "4", "99"} {
		t.Run("exit "+exit, func(t *testing.T) {
			withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_all_ok.json"), "GO_HELPER_STDERR=boom\n", "GO_HELPER_EXIT="+exit)
			stdout, stderr, code := runCLI(t, "widget", "--consumer", "router")
			if code != 1 || stdout != "" || !strings.Contains(stderr, "boom") {
				t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

// pg-desk missing from $PATH: no helper, real exec with an empty PATH.
func TestPgDeskNotStartableFailsWithDiagnosticNamingPgDesk(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	stdout, stderr, code := runCLI(t, "widget", "--consumer", "router")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "pg-desk") {
		t.Errorf("stderr = %q, want it to name pg-desk", stderr)
	}
}

func recordedArgs(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(b), "\n")
}

// The exact pg-desk command line; --cached is never passed (it never
// advances the cursor, so a polling source would see the same records forever).
func TestPgDeskInvocationNeverPassesCached(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_empty.json"), "GO_HELPER_ARGS_FILE="+argsFile)
	if _, stderr, code := runCLI(t, "widget", "--consumer", "router"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	want := []string{"widget", "changes", "--consumer", "router", "--json"}
	if got := recordedArgs(t, argsFile); !reflect.DeepEqual(got, want) {
		t.Errorf("pg-desk argv = %v, want %v", got, want)
	}
}

func TestOptionalQueryAndLimitArePassedThroughButNeverCached(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_empty.json"), "GO_HELPER_ARGS_FILE="+argsFile)
	if _, stderr, code := runCLI(t, "widget", "--consumer", "router", "--query", "open-widgets", "--limit", "5"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	got := recordedArgs(t, argsFile)
	want := []string{"widget", "changes", "--consumer", "router", "--query", "open-widgets", "--limit", "5", "--json"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pg-desk argv = %v, want %v", got, want)
	}
	for _, a := range got {
		if a == "--cached" {
			t.Errorf("--cached must never be passed: %v", got)
		}
	}
}

func TestAdapterDoesNotAcceptCachedFlag(t *testing.T) {
	_, _, code := runCLI(t, "widget", "--consumer", "router", "--cached")
	if code == 0 {
		t.Error("adapter must reject --cached")
	}
}

func TestMissingConsumerOrTypeIsAUsageError(t *testing.T) {
	for _, args := range [][]string{{"widget"}, {"--consumer", "router"}} {
		stdout, stderr, code := runCLI(t, args...)
		if code != 1 || stdout != "" || stderr == "" {
			t.Errorf("args %v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
}

func TestHelpStatesInvocationShape(t *testing.T) {
	stdout, _, code := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "pg-router-source-pg-desk <type> --consumer <name>") {
		t.Errorf("--help lacks the invocation shape:\n%s", stdout)
	}
}

// Every emitted item carries emit "<entity_type>.changed" (pg-router types an
// event from emit, never from the item type).
func TestEveryItemCarriesChangedEmit(t *testing.T) {
	for _, name := range []string{"envelope_degraded.json", "envelope_all_ok.json", "envelope_coalesce.json"} {
		withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(name), "GO_HELPER_EXIT=0")
		stdout, _, code := runCLI(t, "widget", "--consumer", "router")
		if code != 0 {
			t.Fatalf("%s: exit = %d", name, code)
		}
		items := mustItems(t, stdout)
		if len(items) == 0 {
			t.Fatalf("%s: no items", name)
		}
		for _, it := range items {
			md := it["metadata"].(map[string]any)
			if want := md["entity_type"].(string) + ".changed"; it["emit"] != want {
				t.Errorf("%s: emit = %v, want %q", name, it["emit"], want)
			}
		}
	}
}

// Two records for one entity (seq 4 kind ci; seq 7 kinds review, ci) coalesce
// to ONE item with id <entity_id>@7, the coalesced kinds without duplicates in
// order of first appearance, and the max-seq record's title, version and
// origin (the implementer's documented choice: the latest state wins). Items
// keep the order of each entity's first record.
func TestSameEntityRecordsCoalesceIntoOneItemAtMaxSeq(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("envelope_coalesce.json"), "GO_HELPER_EXIT=0")
	stdout, _, code := runCLI(t, "widget", "--consumer", "router")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	items := mustItems(t, stdout)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (one per entity): %s", len(items), stdout)
	}
	first := items[0]
	if first["id"] != "w-1@7" || first["emit"] != "widget.changed" || first["title"] != "Newer title" {
		t.Errorf("first item = %v", first)
	}
	md := first["metadata"].(map[string]any)
	if !reflect.DeepEqual(md["kinds"], []any{"ci", "review"}) {
		t.Errorf("kinds = %#v, want [ci review]", md["kinds"])
	}
	if md["seq"] != float64(7) || md["version"] != float64(3) || md["origin"] != "annotate" {
		t.Errorf("metadata = %v, want seq 7 version 3 origin annotate", md)
	}
	if items[1]["id"] != "w-2@5" {
		t.Errorf("second item id = %v, want w-2@5", items[1]["id"])
	}
}

// Two envelopes for the same entity with different max seq yield different
// item ids, so a change landing while a run is in flight is a distinct event.
func TestSameEntityDifferentMaxSeqYieldsDifferentIDs(t *testing.T) {
	ids := map[string]bool{}
	for _, name := range []string{"envelope_coalesce.json", "envelope_coalesce_later.json"} {
		withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture(name), "GO_HELPER_EXIT=0")
		stdout, _, code := runCLI(t, "widget", "--consumer", "router")
		if code != 0 {
			t.Fatalf("%s: exit = %d", name, code)
		}
		for _, it := range mustItems(t, stdout) {
			if md := it["metadata"].(map[string]any); md["entity_id"] == "w-1" {
				ids[it["id"].(string)] = true
			}
		}
	}
	if !reflect.DeepEqual(ids, map[string]bool{"w-1@7": true, "w-1@9": true}) {
		t.Errorf("ids = %v, want w-1@7 and w-1@9", ids)
	}
}
