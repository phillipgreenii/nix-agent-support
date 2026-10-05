package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "decider.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvVar, p)
}

func TestLoadUnsetVariableYieldsZeroConfig(t *testing.T) {
	t.Setenv(EnvVar, "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.AgentTrackerBackend != "" || c.BeadsDir != "" || c.Actor != "" || c.EscalateAfter != nil {
		t.Fatalf("not the zero config: %+v", c)
	}
	if c.K() != 3 || c.ActorOrDefault() != "pg-decider" {
		t.Fatalf("defaults: K=%d actor=%q", c.K(), c.ActorOrDefault())
	}
}

func TestLoadReadsEveryKey(t *testing.T) {
	writeCfg(t, `{"agent_tracker_backend":"trk","beads_dir":"/tmp/beads","actor":"bot","escalate_after":5}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AgentTrackerBackend != "trk" || c.BeadsDir != "/tmp/beads" || c.Actor != "bot" || c.K() != 5 || c.ActorOrDefault() != "bot" {
		t.Fatalf("got %+v K=%d", c, c.K())
	}
}

func TestActorDefaultsWhenTheFileSetsNone(t *testing.T) {
	writeCfg(t, `{"agent_tracker_backend":"trk"}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ActorOrDefault() != "pg-decider" {
		t.Fatalf("actor = %q", c.ActorOrDefault())
	}
}

func TestEscalateAfterBelowOneFailsLoudly(t *testing.T) {
	for _, v := range []string{"0", "-2"} {
		writeCfg(t, `{"escalate_after":`+v+`}`)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "escalate_after") {
			t.Fatalf("escalate_after %s: err = %v", v, err)
		}
	}
}

func TestEscalateAfterOneIsAccepted(t *testing.T) {
	writeCfg(t, `{"escalate_after":1}`)
	c, err := Load()
	if err != nil || c.K() != 1 {
		t.Fatalf("err=%v c=%+v", err, c)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "missing.json"))
	if _, err := Load(); err == nil {
		t.Fatal("a named but missing file must be an error")
	}
	writeCfg(t, `{not json`)
	if _, err := Load(); err == nil {
		t.Fatal("unparseable file must be an error")
	}
}

func TestLoadToleratesUnknownKeys(t *testing.T) {
	writeCfg(t, `{"actor":"bot","future_key":true}`)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestNilConfigDefaults(t *testing.T) {
	var c *Config
	if c.K() != 3 || c.ActorOrDefault() != "pg-decider" {
		t.Fatalf("nil defaults: K=%d actor=%q", c.K(), c.ActorOrDefault())
	}
}
