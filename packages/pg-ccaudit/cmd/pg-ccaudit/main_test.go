package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/lock"
)

// captureRun drives the CLI end to end. run takes *os.File, so real temp files
// stand in for stdout/stderr.
func captureRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	outPath := filepath.Join(dir, "stdout")
	errPath := filepath.Join(dir, "stderr")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create stdout: %v", err)
	}
	errf, err := os.Create(errPath)
	if err != nil {
		t.Fatalf("create stderr: %v", err)
	}
	runErr := run(context.Background(), args, out, errf)
	if err := out.Close(); err != nil {
		t.Fatalf("close stdout: %v", err)
	}
	if err := errf.Close(); err != nil {
		t.Fatalf("close stderr: %v", err)
	}
	ob, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	eb, err := os.ReadFile(errPath)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return string(ob), string(eb), runErr
}

// corpus builds a minimal transcript tree in a temp dir. The CLI tests must never
// reach the real corpus or the real database, so every invocation below passes
// explicit --root and --db.
func corpus(t *testing.T) (root, dbPath string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "projects", "projA")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `{"type":"assistant","uuid":"x-0","sessionId":"S-CLI","timestamp":"2026-07-22T10:00:00.000Z","isSidechain":false,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_X1","name":"Bash","input":{"command":"sleep 60"}}]}}` + "\n" +
		`{"type":"user","uuid":"x-1","sessionId":"S-CLI","timestamp":"2026-07-22T10:00:01.000Z","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_X1","is_error":true,"content":"Blocked: sleep 60"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "s.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return filepath.Join(base, "projects"), filepath.Join(base, "transcripts.db")
}

func TestIngestThenQueryEndToEnd(t *testing.T) {
	root, dbPath := corpus(t)

	out, _, err := captureRun(t, "ingest", "--root", root, "--db", dbPath, "--final-after", "0")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !strings.Contains(out, "ingest complete: ") {
		t.Fatalf("no summary line:\n%s", out)
	}
	if !strings.Contains(out, "errors=1") {
		t.Errorf("summary does not report the single error:\n%s", out)
	}

	out, errOut, err := captureRun(t, "query", "--root", root, "--db", dbPath, "top-signatures")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(out, "Blocked: sleep N") {
		t.Errorf("query output missing the normalized signature:\n%s", out)
	}
	// The staleness note belongs on STDERR so stdout stays machine-readable.
	if !strings.Contains(errOut, "staleness:") {
		t.Errorf("no staleness note on stderr:\n%s", errOut)
	}
	if strings.Contains(out, "staleness:") {
		t.Errorf("staleness note leaked into stdout:\n%s", out)
	}
}

// T-1: the second sweep with nothing new reports zero work.
func TestSecondIngestReportsZeroWork(t *testing.T) {
	root, dbPath := corpus(t)
	if _, _, err := captureRun(t, "ingest", "--root", root, "--db", dbPath, "--final-after", "0"); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	out, _, err := captureRun(t, "ingest", "--root", root, "--db", dbPath, "--final-after", "0")
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if !strings.Contains(out, "changed=0") || !strings.Contains(out, "bytes=0") {
		t.Errorf("second ingest did not report zero work:\n%s", out)
	}
}

// T-12 at the CLI boundary: a second concurrent ingest exits NON-ERROR having
// done nothing. This is what keeps an overlapping launchd tick from logging an
// alarm — and, more importantly, from racing the first writer's resume offsets.
func TestConcurrentIngestNoOpsWithoutError(t *testing.T) {
	root, dbPath := corpus(t)
	lockPath := lock.DefaultPath(dbPath)
	held, err := lock.TryAcquire(lockPath)
	if err != nil {
		t.Fatalf("hold the lock: %v", err)
	}
	defer func() { _ = held.Release() }()

	out, _, err := captureRun(t, "ingest", "--root", root, "--db", dbPath, "--final-after", "0")
	if err != nil {
		t.Fatalf("concurrent ingest returned an error; an overlapping tick must be a clean no-op: %v", err)
	}
	if !strings.Contains(out, "ingest skipped") {
		t.Errorf("output does not report the skip:\n%s", out)
	}
	// Nothing was written.
	if _, statErr := os.Stat(dbPath); statErr == nil {
		t.Error("the skipped ingest created a database; it must do nothing at all")
	}
}

// T-13 at the CLI boundary: the query path leaves the index byte-identical.
func TestQueryDoesNotModifyTheIndex(t *testing.T) {
	root, dbPath := corpus(t)
	if _, _, err := captureRun(t, "ingest", "--root", root, "--db", dbPath, "--final-after", "0"); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Add an unindexed transcript so the index is genuinely stale.
	if err := os.WriteFile(filepath.Join(root, "projA", "later.jsonl"), []byte(
		`{"type":"assistant","uuid":"y-0","sessionId":"S-LATE","timestamp":"2026-07-29T00:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"later"}]}}`+"\n",
	),
		0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, errOut, err := captureRun(t, "query", "--root", root, "--db", dbPath, "error-rate-by-tool")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(out, "Bash") {
		t.Errorf("query returned no results against a stale index:\n%s", out)
	}
	if !strings.Contains(errOut, "BEHIND") {
		t.Errorf("staleness note does not report the index is behind:\n%s", errOut)
	}
	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the query modified the index: size %d->%d, mtime %s->%s",
			before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
}

func TestStatusCommand(t *testing.T) {
	root, dbPath := corpus(t)
	if _, _, err := captureRun(t, "ingest", "--root", root, "--db", dbPath, "--final-after", "0"); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	out, _, err := captureRun(t, "status", "--root", root, "--db", dbPath)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"staleness:", "lines_bad", "thinking table"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output omits %q:\n%s", want, out)
		}
	}

	out, _, err = captureRun(t, "status", "--root", root, "--db", dbPath, "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	for _, want := range []string{`"files_indexed"`, `"stale"`, `"thinking_table"`} {
		if !strings.Contains(out, want) {
			t.Errorf("status --json omits %q:\n%s", want, out)
		}
	}
}

func TestQueriesCommandListsEveryQuery(t *testing.T) {
	out, _, err := captureRun(t, "queries")
	if err != nil {
		t.Fatalf("queries: %v", err)
	}
	for _, name := range []string{
		"error-rate-by-tool", "top-signatures", "bash-by-lead-cmd", "session-concentration",
		"retry-chains", "error-then-narration", "sidechain-split", "cost-by-signature",
		"hook-rejections", "hook-refusals-in-body", "first-seen", "last-seen",
	} {
		if !strings.Contains(out, name) {
			t.Errorf("`queries` omits %q:\n%s", name, out)
		}
	}
	// Every entry carries its version so a pasted result stays comparable.
	if !strings.Contains(out, "v1") {
		t.Errorf("`queries` omits query versions:\n%s", out)
	}

	out, _, err = captureRun(t, "queries", "--verbose")
	if err != nil {
		t.Fatalf("queries --verbose: %v", err)
	}
	if !strings.Contains(out, "notes:") || !strings.Contains(out, "sql:") {
		t.Errorf("`queries --verbose` omits notes or SQL:\n%s", out)
	}
}

// The cost query's columns are the easiest thing in this tool to misquote, so the
// caveat must travel with it rather than living only in a design document.
func TestCostQueryCarriesItsCaveat(t *testing.T) {
	out, _, err := captureRun(t, "queries", "--verbose")
	if err != nil {
		t.Fatalf("queries --verbose: %v", err)
	}
	for _, want := range []string{"durationMs", "system", "elapsed_ms_sum"} {
		if !strings.Contains(out, want) {
			t.Errorf("cost-by-signature notes omit %q:\n%s", want, out)
		}
	}
}

func TestSchemaCommand(t *testing.T) {
	out, _, err := captureRun(t, "schema")
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	for _, want := range []string{"resume_offset", "content_len", "complete", "is_sidechain", "signature"} {
		if !strings.Contains(out, want) {
			t.Errorf("schema output omits %q", want)
		}
	}
	if strings.Contains(out, "CREATE TABLE IF NOT EXISTS thinking") {
		t.Error("the default schema must not include the optional thinking table")
	}
	out, _, err = captureRun(t, "schema", "--thinking")
	if err != nil {
		t.Fatalf("schema --thinking: %v", err)
	}
	if !strings.Contains(out, "thinking") {
		t.Error("schema --thinking omits the optional table")
	}
}

func TestVersionAndHelp(t *testing.T) {
	out, _, err := captureRun(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "pg-ccaudit ") {
		t.Errorf("version output = %q", out)
	}
	out, _, err = captureRun(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"ingest", "query", "status", "queries"} {
		if !strings.Contains(out, want) {
			t.Errorf("help omits %q", want)
		}
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	_, errOut, err := captureRun(t, "frobnicate")
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestNoArgumentsIsAUsageError(t *testing.T) {
	_, errOut, err := captureRun(t)
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if !strings.Contains(errOut, "USAGE") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestQueryAgainstMissingIndexIsAClearError(t *testing.T) {
	root, dbPath := corpus(t)
	_, _, err := captureRun(t, "query", "--root", root, "--db", dbPath, "top-signatures")
	if err == nil {
		t.Fatal("expected an error when the index does not exist")
	}
	if !strings.Contains(err.Error(), "pg-ccaudit ingest") {
		t.Errorf("the error does not say how to build the index: %v", err)
	}
}

func TestIngestAgainstMissingRootIsAClearError(t *testing.T) {
	_, dbPath := corpus(t)
	_, _, err := captureRun(t, "ingest", "--root", filepath.Join(t.TempDir(), "nope"), "--db", dbPath)
	if err == nil {
		t.Fatal("expected an error for a missing transcript root")
	}
}
