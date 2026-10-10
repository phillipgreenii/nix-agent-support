package view

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const goldenRel = "views/show_issue.golden.json"

// pgDeskGoldenRel is where pg-desk's own golden lives, relative to the repo
// root that contains this module (packages/pg-decider).
var pgDeskGoldenRel = filepath.Join("packages", "pg-desk", "cmd", "pg-desk", "testdata", "show_issue.golden.json")

// annotationJSONNames returns the JSON member names of Annotations.
func annotationJSONNames() map[string]bool {
	names := map[string]bool{}
	t := reflect.TypeOf(Annotations{})
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}

// unknownAnnotationMembers lists annotations members of a printed view that
// are not a field of Annotations.
func unknownAnnotationMembers(t *testing.T, printed []byte) []string {
	t.Helper()
	var w struct {
		Annotations map[string]json.RawMessage `json:"annotations"`
	}
	if err := json.Unmarshal(printed, &w); err != nil {
		t.Fatal(err)
	}
	if len(w.Annotations) == 0 {
		t.Fatal("non-vacuity: printed view has no annotations members")
	}
	known := annotationJSONNames()
	var missing []string
	for k := range w.Annotations {
		if !known[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}

// TestAnnotationsCoverPgDeskGolden fails when pg-desk's printed view carries
// an annotations member that view.Annotations does not decode.
func TestAnnotationsCoverPgDeskGolden(t *testing.T) {
	if missing := unknownAnnotationMembers(t, mustRead(t, goldenRel)); len(missing) != 0 {
		t.Fatalf("view.Annotations lacks fields for golden annotations members: %v", missing)
	}
}

// TestAnnotationDriftCheckCatchesPlantedMember proves the drift check is not
// vacuous: a planted unknown annotations member is reported.
func TestAnnotationDriftCheckCatchesPlantedMember(t *testing.T) {
	golden := mustRead(t, goldenRel)
	planted := bytes.Replace(golden, []byte(`"focus_selected": null,`),
		[]byte(`"focus_selected": null, "planted_unknown_member": 1,`), 1)
	if bytes.Equal(planted, golden) {
		t.Fatal("planting did not change the golden copy")
	}
	got := unknownAnnotationMembers(t, planted)
	if !reflect.DeepEqual(got, []string{"planted_unknown_member"}) {
		t.Fatalf("planted member not reported, got %v", got)
	}
	if len(annotationJSONNames()) == 0 {
		t.Fatal("non-vacuity: Annotations has no json members")
	}
}

// TestGoldenCopyMatchesPgDesk asserts the committed copy is byte-identical to
// pg-desk's golden when the sibling module is reachable. The nix build sandbox
// holds only this module, so the check skips there and runs at commit time.
func TestGoldenCopyMatchesPgDesk(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var src string
	for dir := wd; ; {
		cand := filepath.Join(dir, pgDeskGoldenRel)
		if _, err := os.Stat(cand); err == nil {
			src = cand
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if src == "" {
		t.Skip("pg-desk golden not reachable (sandbox)")
	}
	want, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, goldenRel); !bytes.Equal(got, want) {
		t.Fatalf("testdata/%s differs from %s; copy it again", goldenRel, src)
	}
}
