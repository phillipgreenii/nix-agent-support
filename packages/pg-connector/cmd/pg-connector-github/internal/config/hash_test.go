package config

import "testing"

func TestComputedHashIgnoresKeyOrderWhitespaceAndSpelling(t *testing.T) {
	a := write(t, `{"state_dir":"/tmp/s","summary_ttl":"60s","pr":{"queries":{"mine":"q1","team":["q2","q3"]}},"workers":6}`)
	b := write(t, "{\n  \"workers\": 6,\n\n  \"pr\": {\"queries\": {\"team\": [\"q2\", \"q3\"], \"mine\": \"q1\"}},\n  \"summary_ttl\": \"1m\",\n\t\"state_dir\": \"/tmp/s\"\n}\n")
	ca, err := Load(a, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := Load(b, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if ca.HashStamped() || cb.HashStamped() {
		t.Fatal("neither file is stamped")
	}
	if ca.Hash == "" || ca.Hash != cb.Hash {
		t.Fatalf("hashes differ for equivalent files: %q vs %q", ca.Hash, cb.Hash)
	}
	if len(ca.Hash) != 64 {
		t.Errorf("hash %q is not a sha256 hex string", ca.Hash)
	}
}

func TestComputedHashChangesWithSettings(t *testing.T) {
	a, _ := Load(write(t, `{"state_dir":"/tmp/s","workers":6}`), noEnv)
	b, _ := Load(write(t, `{"state_dir":"/tmp/s","workers":7}`), noEnv)
	c, _ := Load(write(t, `{"state_dir":"/tmp/s","pr":{"queries":{"mine":"x"}}}`), noEnv)
	if a.Hash == b.Hash || a.Hash == c.Hash || b.Hash == c.Hash {
		t.Fatalf("distinct settings must hash apart: %q %q %q", a.Hash, b.Hash, c.Hash)
	}
}

func TestEnvOverridesDoNotChangeHash(t *testing.T) {
	path := write(t, minimal)
	a, _ := Load(path, noEnv)
	b, _ := Load(path, envOf(map[string]string{EnvSocket: "/tmp/x.sock", EnvGraphQLPointsPerHour: "4000", EnvGH: "g"}))
	if a.Hash != b.Hash {
		t.Fatalf("environment overrides must not change the hash: %q vs %q", a.Hash, b.Hash)
	}
}

func TestStampedHashReturnedAsStamped(t *testing.T) {
	cfg, err := Load(write(t, `{"state_dir":"/tmp/s","config_hash":"stamped-by-renderer-123"}`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hash != "stamped-by-renderer-123" || !cfg.HashStamped() || cfg.StampedHash != cfg.Hash {
		t.Fatalf("hash = %q stamped=%v", cfg.Hash, cfg.HashStamped())
	}
}

func TestStampDoesNotEnterComputedHash(t *testing.T) {
	// The computed hash of a file excludes the stamp field, so adding a stamp
	// to a file never changes what an unstamped twin computes.
	plain, err := parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	stamped, err := parse([]byte(`{"state_dir":"/tmp/s","config_hash":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	hp, _ := computeHash(plain)
	hs, _ := computeHash(stamped)
	if hp == "" || hp != hs {
		t.Fatalf("stamp leaked into the computed hash: %q vs %q", hp, hs)
	}
}
