package prepare

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
	"github.com/phillipgreenii/pg-desk-shadow/internal/testutil"
	"github.com/phillipgreenii/pg-desk-shadow/internal/warmup"
)

// buildPgDesk compiles the sibling pg-desk module (only possible in a full
// checkout; the nix test sandbox holds this module alone). With
// PG_DESK_SHADOW_REQUIRE_TOOLS=1 a missing sibling or toolchain FAILS the
// test instead of skipping, so the gate cannot pass vacuously.
func buildPgDesk(t *testing.T) string {
	t.Helper()
	require := os.Getenv("PG_DESK_SHADOW_REQUIRE_TOOLS") == "1"
	if bin := os.Getenv("PG_DESK_SHADOW_PG_DESK_BIN"); bin != "" {
		return bin
	}
	src, _ := filepath.Abs("../../../pg-desk")
	if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
		if require {
			t.Fatalf("sibling pg-desk module not found at %s", src)
		}
		t.Skip("sibling pg-desk module not present (isolated build): set PG_DESK_SHADOW_PG_DESK_BIN or run in a checkout")
	}
	if _, err := exec.LookPath("go"); err != nil {
		if require {
			t.Fatal("go toolchain missing")
		}
		t.Skip("go toolchain missing")
	}
	out := filepath.Join(t.TempDir(), "pg-desk")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/pg-desk")
	cmd.Dir = src
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build pg-desk: %v\n%s", err, b)
	}
	return out
}

const fakeConnector = `#!/bin/sh
# fake pg-connector: answers only the watched-query listing.
case "$*" in
  "pr list --query mine --fingerprints --output json")
    printf '%s\n' '{"entities":[{"id":"acme/api#1","title":"one","stale":false},{"id":"acme/api#2","title":"two","stale":false}],"sources":[{"source":"fake","status":"succeeded"}],"truncated":false,"fingerprints":{"acme/api#1":"fp-one","acme/api#2":"fp-two"}}'
    exit 0;;
esac
printf '%s\n' '{"error":{"code":"unsupported","message":"fake connector"}}'
exit 1
`

type world struct {
	dir, bin string
	env      []string
	db       sqlite.DB
}

func newWorld(t *testing.T, pgDesk string) *world {
	t.Helper()
	testutil.RequireSQLite(t)
	dir := t.TempDir()
	w := &world{dir: dir, bin: filepath.Join(dir, "bin")}
	for _, d := range []string{w.bin, filepath.Join(dir, "state", "pg-desk"), filepath.Join(dir, "run"), filepath.Join(dir, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(pgDesk, filepath.Join(w.bin, "pg-desk")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.bin, "pg-connector"), []byte(fakeConnector), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "self_login: tester\nrepos:\n  - remote: acme/api\nsync:\n  mode: \"off\"\nwatch:\n  pr:\n    queries: [mine]\nsweep:\n  max_age: 8760h\n"
	if err := os.WriteFile(filepath.Join(dir, "pg-desk.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	w.env = []string{
		"PATH=" + w.bin + ":/usr/bin:/bin", "TMPDIR=" + filepath.Join(dir, "tmp"), "XDG_STATE_HOME=" + filepath.Join(dir, "state"),
		"XDG_RUNTIME_DIR=" + filepath.Join(dir, "run"), "PG_DESK_CONFIG=" + filepath.Join(dir, "pg-desk.yaml"), "BEADS_DOLT_AUTO_START=0",
	}
	w.db = sqlite.DB{Path: filepath.Join(dir, "state", "pg-desk", "store.db")}

	// A version-1 store with five never-hydrated pr rows (only #1 and #2 are listed).
	ddl, err := os.ReadFile("testdata/v1-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := w.db.Exec(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&sb, "INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, content_hash) VALUES ('acme/api','pr','acme/api#%d','{}','2026-01-05T09:00:00Z','h');\n", i)
	}
	if err := w.db.Exec(ctx, sb.String()); err != nil {
		t.Fatal(err)
	}
	if out, code := w.run(t, "migrate", "--cutover"); code != 0 {
		t.Fatalf("migrate --cutover: exit %d: %s", code, out)
	}
	return w
}

func (w *world) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(filepath.Join(w.bin, "pg-desk"), args...)
	cmd.Env = w.env
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out) + string(ee.Stderr), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

type changesOut struct {
	Records []struct {
		ID     string   `json:"id"`
		Origin string   `json:"origin"`
		Kinds  []string `json:"kinds"`
	} `json:"records"`
}

func (w *world) poll(t *testing.T) (changesOut, int64, int) {
	t.Helper()
	before, _ := w.db.Int(context.Background(), "SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key='change_flow.hydrations.pr'),0)")
	out, code := w.run(t, "pr", "changes", "--consumer", "shadow-compare", "--json")
	var env changesOut
	if i := strings.Index(out, "{"); i >= 0 {
		_ = json.Unmarshal([]byte(out[i:]), &env)
	}
	after, _ := w.db.Int(context.Background(), "SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key='change_flow.hydrations.pr'),0)")
	return env, after - before, code
}

func TestSeedingTurnsTheRemoteTierOffOnARealMigratedStore(t *testing.T) {
	pgDesk := buildPgDesk(t)
	ctx := context.Background()

	// Without seeding: every never-hydrated row is due for the sweep tier, even
	// with sweep.max_age set to 8760h (the pg2-5rb3t mechanism).
	cold := newWorld(t, pgDesk)
	if n, _ := cold.db.Int(ctx, warmup.NeverHydratedSQL); n != 5 {
		t.Fatalf("after migrate every active pr row is never hydrated, got %d", n)
	}
	_, hyd, _ := cold.poll(t)
	if hyd == 0 {
		t.Fatal("control failed: an unseeded copy must hydrate (the sweep tier ignores max_age for NULL hydrated_at)")
	}

	// With the warm-up: prime from the fake connector's listing, seed, poll.
	w := newWorld(t, pgDesk)
	res, err := exec.Command(filepath.Join(w.bin, "pg-connector"), "pr", "list", "--query", "mine", "--fingerprints", "--output", "json").Output()
	if err != nil {
		t.Fatal(err)
	}
	lst, err := warmup.ParseListing("mine", res)
	if err != nil {
		t.Fatal(err)
	}
	active, err := warmup.Seed(ctx, w.db, warmup.Prime([]warmup.Listing{lst}), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("active rows after seeding = %d, want the 2 listed", active)
	}
	if n, _ := w.db.Int(ctx, warmup.NeverHydratedSQL); n != 0 {
		t.Fatalf("never-hydrated active rows = %d, want 0", n)
	}
	for i := 0; i < 2; i++ {
		env, hyd, code := w.poll(t)
		if code != 0 {
			t.Fatalf("poll %d exited %d", i+1, code)
		}
		if hyd != 0 {
			t.Errorf("poll %d hydrated %d entities, want 0 (the remote tier must be off)", i+1, hyd)
		}
		for _, r := range env.Records {
			if r.Origin == "sweep" || r.Origin == "pg-connector" {
				t.Errorf("poll %d produced a %s record for %s", i+1, r.Origin, r.ID)
			}
		}
	}
}
