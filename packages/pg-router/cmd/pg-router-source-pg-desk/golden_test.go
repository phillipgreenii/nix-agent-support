package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/pkg/deskapi"
)

// deskapiGolden is the published golden directory of the pg-desk contract,
// read in place by a relative path from this module (never copied).
var deskapiGolden = filepath.Join("..", "..", "..", "pg-desk", "pkg", "deskapi", "testdata", "golden")

// TestDecoderOverDeskapiChangesGoldens runs this adapter's decoder over every
// changes golden: the strict decode (unknown members rejected) accepts it, the
// adapter maps it to items, and the ack object of a record that carries one
// decodes into the deskapi ack type.
func TestDecoderOverDeskapiChangesGoldens(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(deskapiGolden, "changes_*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no changes goldens under %s (err %v)", deskapiGolden, err)
	}
	sawAck := false
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			env, err := deskapi.DecodeChanges(data)
			if err != nil {
				t.Fatalf("strict decode (unknown members rejected): %v", err)
			}
			if env.Contract != deskapi.ChangesContract {
				t.Fatalf("contract = %q", env.Contract)
			}
			items, _, err := itemsWithLowest(env)
			if err != nil {
				t.Fatalf("itemsWithLowest: %v", err)
			}
			if len(items) == 0 || len(items) > len(env.Records) {
				t.Fatalf("%d items from %d records", len(items), len(env.Records))
			}
			for _, r := range env.Records {
				if r.Ack != nil {
					sawAck = true
					a := deskapi.Ack(*r.Ack)
					if a.Lane == "" || a.Token == "" {
						t.Fatalf("ack = %+v, want lane and token", a)
					}
				}
			}
		})
	}
	if !sawAck {
		t.Fatal("no changes golden carries an ack object (K8's goldens are expected to)")
	}
}
