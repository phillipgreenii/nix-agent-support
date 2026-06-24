package enrich

import (
	"reflect"
	"testing"
)

func TestDetectLanguages(t *testing.T) {
	t.Run("ranked by count", func(t *testing.T) {
		got := detectLanguages([]string{"a.go", "b.go", "c.py"})
		if !reflect.DeepEqual(got, []string{"Go", "Python"}) {
			t.Fatalf("got %v; want [Go Python]", got)
		}
	})
	t.Run("empty input", func(t *testing.T) {
		if got := detectLanguages(nil); got != nil {
			t.Fatalf("got %v; want nil", got)
		}
	})
	t.Run("nix recognized", func(t *testing.T) {
		got := detectLanguages([]string{"flake.nix"})
		if !reflect.DeepEqual(got, []string{"Nix"}) {
			t.Fatalf("got %v; want [Nix]", got)
		}
	})
}
