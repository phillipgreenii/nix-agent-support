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

// The expected values below were computed BEFORE pr.posted_state_dir existed,
// from these same inputs, and are pinned so that a later change to the encoding
// cannot move them unnoticed: a file that never names the key MUST hash exactly
// as it did before the key was added, because a stamped hash a renderer wrote
// earlier and a computed one are compared across processes.
func TestHashOfFilesWithoutPostedStateDirIsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"minimal file", write(t, minimal), "3d129355277446ce895c82b6bcbc509e375d2658aa0a9b32cdaa547ed45cbdb5"},
		{
			"queries and overrides",
			write(t, `{"state_dir":"/tmp/s","summary_ttl":"90s","workers":6,"pr":{"queries":{"mine":"q1","team":["q2","q3"]}}}`),
			"5d58c8b2f4fafc864f4ec5399a4c25b147ab0b0fddce306e78c0e96fa6fe5554",
		},
		// The golden example now writes the key at its default ("" means unset),
		// and still hashes as it did without the key.
		{"golden example", "testdata/example.json", "31c9b8299e53a7a98ee7a6a3826a4df792b52c1f84d30608d4df7e8dddbd0f00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(tc.path, noEnv)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.HashStamped() || cfg.Hash != tc.want {
				t.Errorf("hash = %q (stamped=%v), want %q", cfg.Hash, cfg.HashStamped(), tc.want)
			}
		})
	}
}

func TestPostedStateDirEntersTheHashOnlyWhenSet(t *testing.T) {
	load := func(body string) *Config {
		t.Helper()
		cfg, err := Load(write(t, body), noEnv)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		return cfg
	}
	unset := load(minimal)
	for name, body := range map[string]string{
		"empty string": `{"state_dir":"/tmp/s","pr":{"posted_state_dir":""}}`,
		"null":         `{"state_dir":"/tmp/s","pr":{"posted_state_dir":null}}`,
		"empty pr":     `{"state_dir":"/tmp/s","pr":{}}`,
	} {
		if got := load(body); got.Hash != unset.Hash || got.PR.PostedStateDir != "" {
			t.Errorf("%s must hash as unset: %q vs %q (value %q)", name, got.Hash, unset.Hash, got.PR.PostedStateDir)
		}
	}
	a := load(`{"state_dir":"/tmp/s","pr":{"posted_state_dir":"/var/example/state-a"}}`)
	b := load(`{"state_dir":"/tmp/s","pr":{"posted_state_dir":"/var/example/state-b"}}`)
	if a.Hash == unset.Hash || a.Hash == b.Hash {
		t.Errorf("a set value is a file setting and must change the hash: unset %q a %q b %q", unset.Hash, a.Hash, b.Hash)
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
