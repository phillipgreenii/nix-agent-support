package event_test

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/schemacheck"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/schemas"
)

// TestEveryEventTypeHasADef checks that the schema's discriminator, its
// $defs and the Go list of event types name the same seventeen types. It is
// an external test: schemacheck is imported by event.
func TestEveryEventTypeHasADef(t *testing.T) {
	var doc struct {
		Properties struct {
			Type struct {
				Enum []string `json:"enum"`
			} `json:"type"`
		} `json:"properties"`
		AllOf []struct {
			If struct {
				Properties struct {
					Type struct {
						Const string `json:"const"`
					} `json:"type"`
				} `json:"properties"`
			} `json:"if"`
			Then struct {
				Properties struct {
					Data struct {
						Ref string `json:"$ref"`
					} `json:"data"`
				} `json:"properties"`
			} `json:"then"`
		} `json:"allOf"`
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(schemas.Event(), &doc); err != nil {
		t.Fatal(err)
	}

	var want []string
	for _, ty := range event.Types() {
		want = append(want, string(ty))
	}
	sort.Strings(want)
	if len(want) != 17 {
		t.Fatalf("event.Types() has %d types, want 17", len(want))
	}

	enum := append([]string(nil), doc.Properties.Type.Enum...)
	sort.Strings(enum)
	if !equal(enum, want) {
		t.Errorf("the discriminator enum is %v, want %v", enum, want)
	}

	var branches []string
	for _, b := range doc.AllOf {
		ty := b.If.Properties.Type.Const
		branches = append(branches, ty)
		if ref := b.Then.Properties.Data.Ref; ref != "#/$defs/"+ty {
			t.Errorf("the branch for %q validates data against %q, want #/$defs/%s", ty, ref, ty)
		}
		if _, ok := doc.Defs[ty]; !ok {
			t.Errorf("the schema has no $defs entry for %q", ty)
		}
	}
	sort.Strings(branches)
	if !equal(branches, want) {
		t.Errorf("the schema has branches for %v, want %v", branches, want)
	}

	for _, ty := range want {
		if _, ok := doc.Defs[ty]; !ok {
			t.Errorf("the schema has no $defs entry for %q", ty)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDecodeChecksTheSchemaBeforeTheGoChecks sends a line that breaks both: a
// period.changed with no batch (the schema's rule) and a zone that is not an
// IANA name (the Go check's). The schema's verdict is the one reported.
func TestDecodeChecksTheSchemaBeforeTheGoChecks(t *testing.T) {
	line := []byte(`{"v":1,"id":"01J9Z3K8M2E000000000000001","at":"2026-10-07T13:30:04.120Z","effective_at":"2026-10-07T13:28:00.000Z","type":"period.changed","data":{"kind":"day","start":"2026-10-07","tz":"ET"}}`)
	_, err := event.Decode(line)
	var verr *schemacheck.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Decode = %v, want a *schemacheck.ValidationError", err)
	}
}

// TestEncodeRefusesALineTheSchemaWouldRefuse keeps the promise that a line the
// library writes is a line it reads: the membership batch is required by the
// schema on a period change, whatever the Go struct allows.
func TestEncodeRefusesALineTheSchemaWouldRefuse(t *testing.T) {
	e := event.Event{
		Envelope: event.Envelope{
			ID:          "01J9Z3K8M2E000000000000001",
			At:          event.At(time.Date(2026, 10, 7, 13, 30, 4, 0, time.UTC)),
			EffectiveAt: event.At(time.Date(2026, 10, 7, 13, 28, 0, 0, time.UTC)),
		},
		Payload: event.PeriodChanged{Kind: "day", Start: civil.Date{Year: 2026, Month: 10, Day: 7}, TZ: "America/New_York"},
	}
	if line, err := event.Encode(e); err == nil {
		t.Fatalf("Encode wrote %s, but Decode would refuse it", line)
	}
	e.Payload = event.PeriodChanged{Kind: "day", Start: civil.Date{Year: 2026, Month: 10, Day: 7}, TZ: "America/New_York", Batch: "01J9Z3K8M2B000000000000001"}
	line, err := event.Encode(e)
	if err != nil {
		t.Fatalf("Encode with a batch: %v", err)
	}
	if _, err := event.Decode(line); err != nil {
		t.Fatalf("Decode(Encode(e)): %v", err)
	}
}
