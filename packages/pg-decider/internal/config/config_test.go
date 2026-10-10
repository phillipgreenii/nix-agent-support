package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "decider.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvVar, p)
}

func TestLoadUnsetVariableYieldsZeroConfig(t *testing.T) {
	t.Setenv(EnvVar, "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.AgentTrackerBackend != "" || c.BeadsDir != "" || c.Actor != "" || c.EscalateAfter != nil {
		t.Fatalf("not the zero config: %+v", c)
	}
	if c.K() != 3 || c.ActorOrDefault() != "pg-decider" {
		t.Fatalf("defaults: K=%d actor=%q", c.K(), c.ActorOrDefault())
	}
}

func TestLoadReadsEveryKey(t *testing.T) {
	writeCfg(t, `{"agent_tracker_backend":"trk","beads_dir":"/tmp/beads","actor":"bot","escalate_after":5}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AgentTrackerBackend != "trk" || c.BeadsDir != "/tmp/beads" || c.Actor != "bot" || c.K() != 5 || c.ActorOrDefault() != "bot" {
		t.Fatalf("got %+v K=%d", c, c.K())
	}
}

func TestActorDefaultsWhenTheFileSetsNone(t *testing.T) {
	writeCfg(t, `{"agent_tracker_backend":"trk"}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ActorOrDefault() != "pg-decider" {
		t.Fatalf("actor = %q", c.ActorOrDefault())
	}
}

func TestEscalateAfterBelowOneFailsLoudly(t *testing.T) {
	for _, v := range []string{"0", "-2"} {
		writeCfg(t, `{"escalate_after":`+v+`}`)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "escalate_after") {
			t.Fatalf("escalate_after %s: err = %v", v, err)
		}
	}
}

func TestEscalateAfterOneIsAccepted(t *testing.T) {
	writeCfg(t, `{"escalate_after":1}`)
	c, err := Load()
	if err != nil || c.K() != 1 {
		t.Fatalf("err=%v c=%+v", err, c)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "missing.json"))
	if _, err := Load(); err == nil {
		t.Fatal("a named but missing file must be an error")
	}
	writeCfg(t, `{not json`)
	if _, err := Load(); err == nil {
		t.Fatal("unparseable file must be an error")
	}
}

func TestLoadToleratesUnknownKeys(t *testing.T) {
	writeCfg(t, `{"actor":"bot","future_key":true}`)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestNilConfigDefaults(t *testing.T) {
	var c *Config
	if c.K() != 3 || c.ActorOrDefault() != "pg-decider" {
		t.Fatalf("nil defaults: K=%d actor=%q", c.K(), c.ActorOrDefault())
	}
}

func TestLoadReadsAreaLabels(t *testing.T) {
	writeCfg(t, `{"area_labels":[
		{"pattern":"^feat\\(widgets\\)","labels":["widgets"," gadgets "]},
		{"pattern":"(?i)PROJ-[0-9]+","field":"branch","labels":["proj","widgets"]}]}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AreaLabels) != 2 || c.AreaLabels[1].Field != "branch" {
		t.Fatalf("got %+v", c.AreaLabels)
	}
	voc := strings.Join(c.AreaVocabulary(), ",")
	if voc != "gadgets,proj,widgets" {
		t.Fatalf("vocabulary = %q", voc)
	}
}

func TestAreaLabelsForUnionsMatchingRules(t *testing.T) {
	writeCfg(t, `{"area_labels":[
		{"pattern":"^feat\\(widgets\\)","labels":["widgets"]},
		{"pattern":"widgets","labels":["any-widget","widgets"]},
		{"pattern":"(?i)PROJ-[0-9]+","field":"branch","labels":["proj"]},
		{"pattern":"PROJ-","labels":["title-only"]}]}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		title, branch, want string
	}{
		{"feat(widgets): add x", "main", "any-widget,widgets"},
		{"fix: y", "me.proj-12.thing", "proj"},
		{"feat(widgets): z", "me.PROJ-9.z", "any-widget,proj,widgets"},
		{"fix(gadgets): nothing", "plain", ""},
		{"PROJ-1 in title", "plain", "title-only"},
	}
	for _, tc := range cases {
		if got := strings.Join(c.AreaLabelsFor(tc.title, tc.branch), ","); got != tc.want {
			t.Errorf("AreaLabelsFor(%q,%q) = %q, want %q", tc.title, tc.branch, got, tc.want)
		}
	}
}

func TestAreaLabelsUnconfiguredOrNilYieldNothing(t *testing.T) {
	var nilCfg *Config
	if nilCfg.AreaLabelsFor("t", "b") != nil || nilCfg.AreaVocabulary() != nil {
		t.Fatal("nil config must yield no labels")
	}
	c := &Config{}
	if c.AreaLabelsFor("t", "b") != nil || c.AreaVocabulary() != nil {
		t.Fatal("empty config must yield no labels")
	}
}

func TestInvalidAreaLabelsFailLoudly(t *testing.T) {
	for name, body := range map[string]string{
		"empty pattern": `{"area_labels":[{"pattern":" ","labels":["a"]}]}`,
		"bad regexp":    `{"area_labels":[{"pattern":"(","labels":["a"]}]}`,
		"bad field":     `{"area_labels":[{"pattern":"x","field":"body","labels":["a"]}]}`,
		"no labels":     `{"area_labels":[{"pattern":"x","labels":[" "]}]}`,
	} {
		writeCfg(t, body)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "area_labels[0]") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestLoadReadsTheFocusKeys(t *testing.T) {
	writeCfg(t, `{"bead_id_pattern":"^pg2-[a-z0-9]+$","focus_beads_query":"focus-beads","focus_priority_map":{"Blocker":"P0","Minor":"P4"}}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	re, err := c.BeadIDRegexp()
	if err != nil || re == nil {
		t.Fatalf("BeadIDRegexp = %v, %v", re, err)
	}
	if !re.MatchString("pg2-ab12") || re.MatchString("PROJ-12") {
		t.Fatal("bead_id_pattern does not tell a bead id from another issue id")
	}
	if c.FocusBeadsQuery != "focus-beads" {
		t.Fatalf("FocusBeadsQuery = %q", c.FocusBeadsQuery)
	}
	// A configured map replaces the default whole.
	for name, want := range map[string]string{"Blocker": "P0", "Minor": "P4", "Highest": "P2", "": "P2"} {
		if got := c.FocusPriority(name); got != want {
			t.Errorf("FocusPriority(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFocusKeysAbsentByDefault(t *testing.T) {
	t.Setenv(EnvVar, "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	re, err := c.BeadIDRegexp()
	if re != nil || err != nil || c.FocusBeadsQuery != "" {
		t.Fatalf("unset keys: re=%v err=%v query=%q", re, err, c.FocusBeadsQuery)
	}
	var nilCfg *Config
	if re, err := nilCfg.BeadIDRegexp(); re != nil || err != nil {
		t.Fatalf("nil config: %v %v", re, err)
	}
}

func TestFocusPriorityDefaultMap(t *testing.T) {
	for _, c := range []*Config{nil, {}, {FocusPriorityMap: nil}} {
		for name, want := range map[string]string{
			"Highest": "P0", "High": "P1", "Medium": "P2", "Low": "P3", "Lowest": "P4",
			"Unheard-of": "P2", "": "P2",
		} {
			if got := c.FocusPriority(name); got != want {
				t.Errorf("FocusPriority(%q) = %q, want %q", name, got, want)
			}
		}
	}
}

func TestInvalidFocusKeysFailLoudly(t *testing.T) {
	for name, tc := range map[string]struct{ body, mention string }{
		"uncompilable pattern":  {`{"bead_id_pattern":"(unclosed"}`, "bead_id_pattern"},
		"blank pattern":         {`{"bead_id_pattern":"  "}`, "bead_id_pattern"},
		"priority out of range": {`{"focus_priority_map":{"High":"P5"}}`, "focus_priority_map"},
		"priority not a P-code": {`{"focus_priority_map":{"High":"urgent"}}`, "focus_priority_map"},
		"blank priority name":   {`{"focus_priority_map":{" ":"P1"}}`, "focus_priority_map"},
	} {
		writeCfg(t, tc.body)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.mention) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
