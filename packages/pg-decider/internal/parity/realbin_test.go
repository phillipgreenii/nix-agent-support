package parity

import (
	"errors"
	"testing"
)

// realEnv returns an Env over the built binaries, or skips the test with an
// explicit message when EnvFromProcess reports they were not provided, so a
// plain `go test ./...` stays green.
func realEnv(t *testing.T) Env {
	t.Helper()
	env, ok := EnvFromProcess()
	if !ok {
		t.Skip("PG_DECIDER_PARITY_PG_DESK_BIN, PG_DECIDER_PARITY_PG_CONNECTOR_BIN and " +
			"PG_DECIDER_PARITY_PG_DECIDER_BIN are not all set: skipping the test that runs the real binaries")
	}
	env.TempDir = t.TempDir()
	return env
}

// scenarioByPrefix returns the scenario whose name starts with prefix.
func scenarioByPrefix(t *testing.T, prefix string) Scenario {
	t.Helper()
	scs, err := Scenarios()
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range scs {
		if len(sc.Name) >= len(prefix) && sc.Name[:len(prefix)] == prefix {
			return sc
		}
	}
	t.Fatalf("no scenario starts with %q", prefix)
	return Scenario{}
}

func isUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }
