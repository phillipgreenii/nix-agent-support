package schemas

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LevelIngest is the seam the real router ingest fills (the level-projection
// work): it takes the raw bytes of one level-answer object and returns an error
// when the ingest rejects it. Until a real ingest sets levelIngestUnderTest the
// ingest-side assertions skip with an explicit message.
type LevelIngest func(raw []byte) error

// levelIngestUnderTest is set by the real ingest's test wiring; nil means the
// ingest does not exist yet.
var levelIngestUnderTest LevelIngest

func levelGoldens(t *testing.T, suffix string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "level-answer", "*"+suffix))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no goldens matching *%s (err=%v)", suffix, err)
	}
	return paths
}

func TestLevelAnswer_PositiveGoldensValidate(t *testing.T) {
	for _, p := range levelGoldens(t, ".golden.json") {
		t.Run(filepath.Base(p), func(t *testing.T) {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateLevelAnswer(raw); err != nil {
				t.Fatalf("positive golden rejected: %v", err)
			}
		})
	}
}

func TestLevelAnswer_NegativeGoldensRejected(t *testing.T) {
	for _, p := range levelGoldens(t, ".negative.json") {
		t.Run(filepath.Base(p), func(t *testing.T) {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateLevelAnswer(raw)
			if err == nil {
				t.Fatalf("negative golden accepted")
			}
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("error does not wrap ErrValidation: %v", err)
			}
		})
	}
}

func TestLevelAnswer_ErrorNamesField(t *testing.T) {
	cases := map[string]string{
		"malformed-level.negative.json":      "level",
		"missing-observed-at.negative.json":  "observed_at",
		"valid-for-zero.negative.json":       "valid_for_s",
		"empty-owner.negative.json":          "owner",
		"owner-65.negative.json":             "owner",
		"reading-missing-unit.negative.json": "unit",
	}
	for file, field := range cases {
		raw, err := os.ReadFile(filepath.Join("testdata", "level-answer", file))
		if err != nil {
			t.Fatal(err)
		}
		err = ValidateLevelAnswer(raw)
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: error %v does not name %q", file, err, field)
		}
	}
}

func TestLevelAnswer_ValidatorIsValidateBytes(t *testing.T) {
	raw := []byte(`{"schemaVersion":"1"}`)
	a, b := ValidateLevelAnswer(raw), ValidateBytes(LevelAnswerSchema, raw)
	if a == nil || b == nil || a.Error() != b.Error() {
		t.Fatalf("ValidateLevelAnswer %v differs from ValidateBytes %v", a, b)
	}
	if err := ValidateLevelAnswer([]byte(`not json`)); !errors.Is(err, ErrValidation) {
		t.Fatalf("malformed JSON: %v", err)
	}
}

// TestLevelAnswer_IngestAgrees is the ingest side of the both-sides contract:
// positive goldens accepted and negative goldens rejected by the real ingest.
func TestLevelAnswer_IngestAgrees(t *testing.T) {
	if levelIngestUnderTest == nil {
		t.Skip("level ingest not implemented yet: the level-projection work sets levelIngestUnderTest")
	}
	for _, p := range levelGoldens(t, ".golden.json") {
		raw, _ := os.ReadFile(p)
		if err := levelIngestUnderTest(raw); err != nil {
			t.Errorf("ingest rejected positive golden %s: %v", filepath.Base(p), err)
		}
	}
	for _, p := range levelGoldens(t, ".negative.json") {
		raw, _ := os.ReadFile(p)
		if err := levelIngestUnderTest(raw); err == nil {
			t.Errorf("ingest accepted negative golden %s", filepath.Base(p))
		}
	}
}
