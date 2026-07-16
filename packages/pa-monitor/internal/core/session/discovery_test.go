package session

import (
	"os"
	"testing"
)

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

func mustWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(dir+"/"+name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverReadsFilesAndKeepsDeadPids(t *testing.T) {
	dir := t.TempDir()
	// Write three session files. PIDs: 100 (alive), 200 (dead), 300 (alive).
	mustWrite(t, dir, "a.json", `{"pid":100,"sessionId":"a","cwd":"/p"}`)
	mustWrite(t, dir, "b.json", `{"pid":200,"sessionId":"b","cwd":"/p"}`)
	mustWrite(t, dir, "c.json", `{"pid":300,"sessionId":"c","cwd":"/p"}`)

	d := Discoverer{
		SessionsDir: dir,
		PidAlive: func(pid int) bool {
			return pid == 100 || pid == 300
		},
	}
	out, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d sessions, want 3 (no dead-PID filter)", len(out))
	}
	byID := map[string]*Session{}
	for _, s := range out {
		byID[s.SessionID] = s
	}
	if !byID["a"].PidAlive {
		t.Errorf("session a: PidAlive false, want true")
	}
	if byID["b"].PidAlive {
		t.Errorf("session b: PidAlive true, want false")
	}
	if !byID["c"].PidAlive {
		t.Errorf("session c: PidAlive false, want true")
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

func TestDiscover_PopulatesEnvViaInjectedReader(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir+"/1.json", `{"pid":42,"sessionId":"s1","cwd":"/x"}`); err != nil {
		t.Fatal(err)
	}
	envByPID := map[int]map[string]string{
		42: {"TMUX": "/tmp/tmux-501/default,1,0", "CMUX_WORKSPACE_ID": "ws1"},
	}
	d := &Discoverer{
		SessionsDir: dir,
		PidAlive:    func(int) bool { return true },
		ReadEnv: func(pid int) (map[string]string, error) {
			return envByPID[pid], nil
		},
	}
	got, err := d.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions", len(got))
	}
	if got[0].Env["TMUX"] != "/tmp/tmux-501/default,1,0" {
		t.Errorf("env TMUX = %q", got[0].Env["TMUX"])
	}
	if got[0].Env["CMUX_WORKSPACE_ID"] != "ws1" {
		t.Errorf("env CMUX_WORKSPACE_ID = %q", got[0].Env["CMUX_WORKSPACE_ID"])
	}
}

func TestDiscover_EmptyEnvOnReaderFailure(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir+"/1.json", `{"pid":42,"sessionId":"s1"}`); err != nil {
		t.Fatal(err)
	}
	d := &Discoverer{
		SessionsDir: dir,
		PidAlive:    func(int) bool { return true },
		ReadEnv: func(pid int) (map[string]string, error) {
			return nil, os.ErrPermission
		},
	}
	got, err := d.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions", len(got))
	}
	if len(got[0].Env) != 0 {
		t.Errorf("expected empty env on reader failure, got %+v", got[0].Env)
	}
}
