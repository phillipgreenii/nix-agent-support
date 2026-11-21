package view

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/pkg/deskapi"
)

// deskapiGolden is K8's published golden directory, read in place by a
// relative path from this module (never copied).
var deskapiGolden = filepath.Join("..", "..", "..", "pg-desk", "pkg", "deskapi", "testdata", "golden")

// TestDecoderOverDeskapiViewGoldens runs this module's decoder over every view
// golden of the published contract: the strict deskapi decode accepts it and
// Parse reads it.
func TestDecoderOverDeskapiViewGoldens(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(deskapiGolden, "view_*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no view goldens under %s (err %v)", deskapiGolden, err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			want, err := deskapi.DecodeView(data)
			if err != nil {
				t.Fatalf("strict decode (unknown members rejected): %v", err)
			}
			got, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got.Contract != deskapi.ViewContract || got.Type != want.Type || got.ID != want.ID ||
				got.Version != want.Version || len(got.Links) != len(want.Links) {
				t.Fatalf("Parse = %+v, deskapi = %+v", got, want)
			}
		})
	}
}
