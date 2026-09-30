package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// runMigrateCmd runs `pg-desk migrate` with --cutover set as given.
func runMigrateCmd(t *testing.T, cutover bool) (stdout string, err error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"migrate"})
	if ferr != nil || c == rootCmd {
		t.Fatalf("rootCmd has no migrate subcommand: %v", ferr)
	}
	flag := c.Flags().Lookup("cutover")
	if flag == nil {
		t.Fatalf("migrate has no --cutover flag")
	}
	val := "false"
	if cutover {
		val = "true"
	}
	if err := c.Flags().Set("cutover", val); err != nil {
		t.Fatalf("set --cutover: %v", err)
	}
	t.Cleanup(func() { _ = c.Flags().Set("cutover", "false") })

	var buf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&buf)
	err = c.RunE(c, nil)
	return buf.String(), err
}

func TestMigrateCutoverCmdMigratesAndSecondRunIsNoOp(t *testing.T) {
	path := storeAtVersion(t, "old")
	withRawStoreAt(t, path)

	out, err := runMigrateCmd(t, true)
	if err != nil {
		t.Fatalf("migrate --cutover: %v", err)
	}
	if !strings.Contains(out, "schema version 2") {
		t.Errorf("first run output does not report schema version 2: %q", out)
	}
	verify := func() {
		t.Helper()
		st, err := store.OpenRaw(path)
		if err != nil {
			t.Fatalf("OpenRaw: %v", err)
		}
		defer func() { _ = st.Close() }()
		if v, err := st.SchemaVersion(); err != nil || v != 2 {
			t.Fatalf("SchemaVersion = %d, %v; want 2", v, err)
		}
		if err := st.RequireNewSchema(); err != nil {
			t.Fatalf("RequireNewSchema after migrate: %v", err)
		}
		if _, found, err := st.GetEntity("o/r", entityTypePR, "o/r#1"); err != nil || !found {
			t.Fatalf("seeded entity lost across the cutover: found=%v err=%v", found, err)
		}
	}
	verify()

	out, err = runMigrateCmd(t, true)
	if err != nil {
		t.Fatalf("second migrate --cutover must be a no-op, got: %v", err)
	}
	if !strings.Contains(out, "already") {
		t.Errorf("second run output does not say the store is already migrated: %q", out)
	}
	verify()
}

func TestMigrateCmdRequiresCutoverFlag(t *testing.T) {
	path := storeAtVersion(t, "old")
	withRawStoreAt(t, path)

	_, err := runMigrateCmd(t, false)
	if err == nil || !strings.Contains(err.Error(), "--cutover") {
		t.Fatalf("migrate without --cutover = %v, want an error naming --cutover", err)
	}
	st, err := store.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer func() { _ = st.Close() }()
	if v, _ := st.SchemaVersion(); v != 1 {
		t.Fatalf("migrate without --cutover changed the store to version %d", v)
	}
}

func TestMigrateCutoverCmdFailsOnUninitializedStore(t *testing.T) {
	withRawStoreAt(t, storeAtVersion(t, "empty"))
	if _, err := runMigrateCmd(t, true); err == nil {
		t.Fatalf("migrate --cutover on an uninitialized store must fail")
	}
}
