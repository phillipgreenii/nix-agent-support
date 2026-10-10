// Package testutil holds the helpers the test packages of pg-task-focus share:
// the example configuration, the ids a fixture draws, the check that an error
// is a refusal, and a pointer to a value. It is imported by external test
// packages (package x_test) only, which is how it can import command without a
// cycle; an internal test package that command itself imports, such as
// projection's, keeps its own helpers.
package testutil

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// ConfigFixture is the example configuration, as a test in internal/<package>
// reaches it.
const ConfigFixture = "../../testdata/config/valid.json"

// LoadConfig is the example configuration after edit has changed its generic
// tree; a nil edit leaves it as it is. It reads ConfigFixture, so it MUST be
// called from a test of a package directly under internal.
func LoadConfig(t testing.TB, edit func(c map[string]any)) *config.Config {
	t.Helper()
	raw, err := os.ReadFile(ConfigFixture)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		var c map[string]any
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		edit(c)
		if raw, err = json.Marshal(c); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

// IDOf is the nth id of a family at instant base: ids of different families,
// or of different numbers, never collide, and the same arguments always give
// the same id.
func IDOf(base time.Time, family byte, n uint32) event.ID {
	var entropy [10]byte
	entropy[0] = family
	binary.BigEndian.PutUint32(entropy[6:], n)
	return event.NewID(base, bytes.NewReader(entropy[:]))
}

// RejectionOf is the rejection err MUST be, with the reason want. It returns
// the rejection by value, so the caller may ignore it.
func RejectionOf(t testing.TB, err error, want command.Reason) command.Rejection {
	t.Helper()
	var r *command.Rejection
	if !errors.As(err, &r) {
		t.Fatalf("error %v (%T) is not a *command.Rejection", err, err)
	}
	if r.Reason != want {
		t.Fatalf("Reason = %q (%s), want %q", r.Reason, r.Message, want)
	}
	return *r
}

// Ptr is a pointer to a copy of v.
func Ptr[T any](v T) *T { return &v }
