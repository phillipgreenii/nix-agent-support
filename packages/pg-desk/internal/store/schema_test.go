package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestFreshStoreShape pins the schema a fresh store is created with to a
// golden that was captured from the retired version-1 ladder plus the cutover,
// so the single creation script and the two steps it replaced describe the
// same tables, columns, defaults, keys and indexes.
func TestFreshStoreShape(t *testing.T) {
	s := OpenForTest(t)
	got := schemaShape(t, s)
	want, err := os.ReadFile(filepath.Join("testdata", "schema2-shape.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("fresh store shape differs from testdata/schema2-shape.golden\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestOpenCreatesSchema2Directly(t *testing.T) {
	s := OpenForTest(t)
	if v, err := s.SchemaVersion(); err != nil || v != 2 {
		t.Fatalf("SchemaVersion = %d, %v; want 2", v, err)
	}
	if err := s.RequireNewSchema(); err != nil {
		t.Fatalf("RequireNewSchema on a fresh store: %v", err)
	}
}

// A store at the retired version 1 is refused, with the error callers test
// for and advice that names the release that still has the cutover.
func TestOpenRefusesRetiredSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	raw, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.sql.Exec(`CREATE TABLE entity (x TEXT); PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	if _, err := Open(path); !errors.Is(err, ErrOldSchema) || !strings.Contains(err.Error(), "no longer migrates schema version 1") {
		t.Fatalf("Open of a version-1 store = %v, want ErrOldSchema with advice", err)
	}
	// The refusal leaves the file alone.
	raw, err = OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if v, _ := raw.SchemaVersion(); v != 1 {
		t.Fatalf("user_version after refusal = %d, want 1", v)
	}
	if err := raw.RequireNewSchema(); !errors.Is(err, ErrOldSchema) {
		t.Fatalf("RequireNewSchema on version 1 = %v, want ErrOldSchema", err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	raw, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.sql.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("Open of a version-3 store = %v, want a newer-than-binary refusal", err)
	}
}

// OpenRaw has no version gate: it opens an empty file, a retired store and a
// current one alike.
func TestOpenRawSkipsTheVersionGate(t *testing.T) {
	r := OpenRetiredSchemaForTest(t)
	if v, err := r.SchemaVersion(); err != nil || v != 1 {
		t.Fatalf("SchemaVersion = %d, %v; want 1", v, err)
	}
	empty, err := OpenRaw(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = empty.Close() }()
	if v, err := empty.SchemaVersion(); err != nil || v != 0 {
		t.Fatalf("SchemaVersion of an empty file = %d, %v; want 0", v, err)
	}
	if err := empty.RequireNewSchema(); !errors.Is(err, ErrOldSchema) {
		t.Fatalf("RequireNewSchema on an empty file = %v, want ErrOldSchema", err)
	}
}

// A failure in any creation step leaves the file empty (version 0, no tables),
// so the next Open starts clean instead of finding half a schema.
func TestCreateSchemaFailureLeavesStoreEmpty(t *testing.T) {
	for i := range schemaStatements {
		steps := append([]string(nil), schemaStatements...)
		steps[i] = `THIS IS NOT SQL`
		s, err := openDB(filepath.Join(t.TempDir(), "x.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.createSchemaWith(steps); err == nil || !strings.Contains(err.Error(), "store left empty") {
			t.Fatalf("step %d: createSchemaWith = %v, want a step failure", i+1, err)
		}
		if v, _ := s.SchemaVersion(); v != 0 {
			t.Errorf("step %d: user_version = %d after failure, want 0", i+1, v)
		}
		var n int
		if err := s.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&n); err != nil || n != 0 {
			t.Errorf("step %d: %d schema objects after failure (%v), want 0", i+1, n, err)
		}
		_ = s.Close()
	}
}

// Several handles on the same new file all run the schema check at once:
// exactly one creates the schema, the others see version 2 under the write
// lock and leave it alone. (The handles are opened first, one after another:
// SQLite's own journal-mode switch on a brand-new file can refuse concurrent
// openers, which is not what this test is about.)
func TestConcurrentSchemaChecksCreateOneSchema(t *testing.T) {
	SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "race.db")
	const n = 4
	handles := make([]*Store, n)
	for i := range n {
		h, err := openDB(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = h.Close() }()
		handles[i] = h
	}
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = checkSchema(handles[i])
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("checkSchema #%d: %v", i, err)
		}
	}
	if got := schemaShape(t, handles[0]); !strings.HasPrefix(got, "user_version=2\n") {
		t.Fatalf("shape after the race:\n%s", got)
	}
}

func TestCreateSchemaIsANoOpOnAnExistingStore(t *testing.T) {
	s := OpenForTest(t)
	before := dumpDB(t, s)
	if err := createSchema(s); err != nil {
		t.Fatalf("createSchema on a created store: %v", err)
	}
	if after := dumpDB(t, s); after != before {
		t.Fatal("createSchema changed an existing store")
	}
}

func TestCheckSchemaReadError(t *testing.T) {
	s := OpenForTest(t)
	_ = s.Close()
	if err := checkSchema(s); err == nil {
		t.Fatal("checkSchema on a closed handle returned no error")
	}
	if err := s.createSchemaWith(nil); err == nil {
		t.Fatal("createSchemaWith on a closed handle returned no error")
	}
}
