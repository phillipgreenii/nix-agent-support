package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunWithoutEnvironmentExitsTwoWithAUsageMessage(t *testing.T) {
	for _, v := range []string{"PG_DECIDER_PARITY_PG_DESK_BIN", "PG_DECIDER_PARITY_PG_CONNECTOR_BIN", "PG_DECIDER_PARITY_PG_DECIDER_BIN"} {
		t.Setenv(v, "")
	}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "PG_DECIDER_PARITY_PG_DESK_BIN") {
		t.Errorf("stderr does not name the variables: %q", errOut.String())
	}
}
