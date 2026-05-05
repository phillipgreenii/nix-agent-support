package session

import (
	"os"
	"testing"
)

// fake pidAlive hook for tests
func fakePidAlive(alive map[int]bool) func(int) bool {
	return func(pid int) bool { return alive[pid] }
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

func TestDiscoverReadsFilesAndFiltersDeadPids(t *testing.T) {
	d := &Discoverer{
		SessionsDir: "../../tests/fixtures/sessions",
		PidAlive:    fakePidAlive(map[int]bool{12345: true, 67890: false}),
	}
	got, err := d.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 live session, got %d", len(got))
	}
	if got[0].PID != 12345 || got[0].SessionID != "abc-def" || got[0].Name != "demo" {
		t.Errorf("unexpected session: %+v", got[0])
	}
}

func TestDiscoverSkipsMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir+"/1.json", `{"pid":1,"sessionId":"s1","cwd":"/x","kind":"interactive"}`); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(dir+"/2.json", `{not json`); err != nil {
		t.Fatal(err)
	}
	d := &Discoverer{
		SessionsDir: dir,
		PidAlive:    func(int) bool { return true },
	}
	got, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover should not fail on malformed file, got %v", err)
	}
	if len(got) != 1 {
		t.Errorf("want 1 session (malformed skipped), got %d", len(got))
	}
}
