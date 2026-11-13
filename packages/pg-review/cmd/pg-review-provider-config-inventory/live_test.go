//go:build live

package main

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
	"github.com/phillipgreenii/x/exitcode"
)

// TestLiveRealConfiguration runs the provider once against the real user
// configuration and the working directory as the scope root. It checks only
// the exit code and the output schema, and logs nothing from the output.
// On demand: go test -tags live ./cmd/pg-review-provider-config-inventory/
func TestLiveRealConfiguration(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{
		"--since", now.Add(-14 * 24 * time.Hour).Format(time.RFC3339), "--until", now.Format(time.RFC3339),
		"--scope", "live", "--scope-prefix", wd, "--layer", "user", "--layer", "rules", "--layer", "project",
	}, &out, &errb)
	if code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
	if err := schema.Validate("evidence-rows", out.Bytes()); err != nil {
		t.Error(err)
	}
}
