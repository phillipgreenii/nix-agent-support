package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestAreaLabelsFor(t *testing.T) {
	cfg := &Config{AreaLabels: []AreaLabelRule{
		{Pattern: `^feat\(svc/alpha\)`, Labels: []string{"alpha-sub", "alpha"}},
		{Pattern: `(?i)beta-[0-9]+`, Field: AreaFieldBranch, Labels: []string{"beta"}},
		{Pattern: `alpha`, Labels: []string{"alpha", " "}}, // duplicate + blank
	}}
	for name, tc := range map[string]struct {
		title, branch string
		want          []string
	}{
		"title rule":                   {"feat(svc/alpha): x", "main", []string{"alpha", "alpha-sub"}},
		"branch rule only":             {"fix: y", "user.BETA-7.z", []string{"beta"}},
		"both, sorted and deduped":     {"feat(svc/alpha): x", "user.beta-7.z", []string{"alpha", "alpha-sub", "beta"}},
		"title pattern ignores branch": {"fix: y", "feat(svc/alpha)", nil},
		"no match":                     {"fix: y", "main", nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := cfg.AreaLabelsFor(tc.title, tc.branch); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("AreaLabelsFor(%q, %q) = %v, want %v", tc.title, tc.branch, got, tc.want)
			}
		})
	}
}

func TestAreaLabels_NilAndEmpty(t *testing.T) {
	var nilCfg *Config
	if nilCfg.AreaLabelsFor("t", "b") != nil || nilCfg.AreaVocabulary() != nil {
		t.Fatal("nil Config must yield nil")
	}
	if got := (&Config{}).AreaLabelsFor("t", "b"); got != nil {
		t.Fatalf("empty config labels = %v, want nil", got)
	}
}

func TestAreaVocabulary(t *testing.T) {
	cfg := &Config{AreaLabels: []AreaLabelRule{
		{Pattern: "a", Labels: []string{"zeta", "alpha"}},
		{Pattern: "b", Labels: []string{"alpha", "beta"}},
	}}
	if got, want := cfg.AreaVocabulary(), []string{"alpha", "beta", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AreaVocabulary = %v, want %v", got, want)
	}
}

func TestLoadFile_AreaLabels(t *testing.T) {
	base := "self_login: a\nrepos:\n  - remote: o/r\n"
	cfg, err := LoadFile(writeYAML(t, t.TempDir(), base+"area_labels:\n  - pattern: 'x+'\n    labels: [one, two]\n  - pattern: 'y'\n    field: branch\n    labels: [three]\n"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(cfg.AreaLabels) != 2 || cfg.AreaLabels[1].Field != "branch" || !reflect.DeepEqual(cfg.AreaLabels[0].Labels, []string{"one", "two"}) {
		t.Fatalf("area_labels parsed as %+v", cfg.AreaLabels)
	}
	for name, block := range map[string]string{
		"empty pattern": "  - pattern: ''\n    labels: [a]\n",
		"bad regexp":    "  - pattern: '('\n    labels: [a]\n",
		"unknown field": "  - pattern: x\n    field: author\n    labels: [a]\n",
		"no labels":     "  - pattern: x\n    labels: []\n",
		"blank labels":  "  - pattern: x\n    labels: ['  ']\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFile(writeYAML(t, t.TempDir(), base+"area_labels:\n"+block))
			if err == nil || !strings.Contains(err.Error(), "area_labels[0]") {
				t.Fatalf("LoadFile err = %v, want an area_labels[0] validation error", err)
			}
		})
	}
}
