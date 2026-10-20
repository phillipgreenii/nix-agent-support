package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
)

// cutoverFromDir synthesises a scratch directory, loads it, lets mutate edit the
// loaded input and computes the phase report.
func cutoverFromDir(t *testing.T, o SynthOptions, mutate func(*Input)) PhaseReport {
	t.Helper()
	dir := t.TempDir()
	if _, err := Synthesize(dir, o); err != nil {
		t.Fatal(err)
	}
	in, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&in)
	}
	return Compute(in, params())
}

// cleanRun edits the synthetic scratch so every criterion is met: the two
// unexplained live events are dropped and the daemon items are seen earlier than
// the live enqueue (shadow delay from the PR's update 30 s, live 60 and 70 s).
func cleanRun(in *Input) {
	var ev []Dispatch
	for _, d := range in.Events {
		if d.ID == synthID(102) || d.ID == synthID(108) {
			continue
		}
		ev = append(ev, d)
	}
	in.Events = ev
	for i := range in.Rows {
		for j := range in.Rows[i].Items {
			it := &in.Rows[i].Items[j]
			if it.Origin != schema.OriginDaemonFeed {
				continue
			}
			if at, ok := schema.Time(it.At); ok {
				it.At = schema.Format(at.Add(-60 * time.Second))
			}
		}
	}
}

func metOf(p PhaseReport) [4]bool {
	var m [4]bool
	for i, c := range p.Cutover.Criteria {
		m[i] = c.Met
	}
	return m
}

func crit(p PhaseReport, name string) CutoverCriterion {
	for _, c := range p.Cutover.Criteria {
		if c.Name == name {
			return c
		}
	}
	return CutoverCriterion{}
}

func TestCutoverAllFourCriteriaMet(t *testing.T) {
	p := cutoverFromDir(t, SynthOptions{Seeded: true}, cleanRun)
	if !cutoverOrderOK(p.Cutover) {
		t.Fatalf("criteria not in the fixed order: %+v", p.Cutover.Criteria)
	}
	if got := metOf(p); got != [4]bool{true, true, true, true} {
		t.Fatalf("met = %v, criteria %+v", got, p.Cutover.Criteria)
	}
	if len(p.Cutover.FollowUps) != 0 || p.Cutover.FollowUps == nil {
		t.Errorf("no unmet criterion means an empty, non-nil follow-up list: %#v", p.Cutover.FollowUps)
	}
	md := Markdown(Report{Phases: []PhaseReport{p}})
	if strings.Contains(md, "NOT MET") || strings.Contains(md, "### Follow-ups") {
		t.Errorf("an all-met report must carry no NOT MET line and no follow-ups heading:\n%s", md)
	}
	if !strings.Contains(md, "- MET: "+CutoverUptime+": 172 of 179 slots up (172 / 179 = 96.1 percent)") {
		t.Errorf("uptime line missing its figure:\n%s", md)
	}
	if !strings.Contains(crit(p, CutoverUptime).Evidence, "172 x 100 = 17200 >= 95 x 179 = 17005") {
		t.Errorf("uptime arithmetic: %q", crit(p, CutoverUptime).Evidence)
	}
}

func TestCutoverEachCriterionUnmetInIsolation(t *testing.T) {
	cases := []struct {
		name   string
		idx    int
		mutate func(*Input)
	}{
		{"unexplained miss", 0, func(in *Input) {
			cleanRun(in)
			in.Events = append(in.Events, Dispatch{Time: time.Date(2026, 1, 5, 10, 20, 20, 0, time.UTC), EventType: "pr.changed", EnqueuedAt: time.Date(2026, 1, 5, 10, 20, 0, 0, time.UTC), ID: synthID(102), Raw: synthID(102)})
		}},
		{"shadow delay worse than live", 1, func(in *Input) {
			cleanRun(in)
			for i := range in.Rows {
				for j := range in.Rows[i].Items {
					it := &in.Rows[i].Items[j]
					if it.Origin == schema.OriginDaemonFeed {
						at, _ := schema.Time(it.At)
						it.At = schema.Format(at.Add(120 * time.Second))
					}
				}
			}
		}},
		{"an hour over the cap", 2, func(in *Input) {
			cleanRun(in)
			for i := range in.Rows {
				if in.Rows[i].Kind == schema.KindTick && in.Rows[i].Slot == schema.Format(time.Date(2026, 1, 5, 11, 30, 0, 0, time.UTC)) {
					in.Rows[i].GraphQLCostSum = 1501
				}
			}
		}},
		{"daemon down for many slots", 3, func(in *Input) {
			cleanRun(in)
			n := 0
			for i := range in.Rows {
				if in.Rows[i].Kind == schema.KindTick && in.Rows[i].Daemon != nil && n < 10 {
					in.Rows[i].Daemon.State = schema.DaemonDown
					n++
				}
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := cutoverFromDir(t, SynthOptions{Seeded: true}, c.mutate)
			want := [4]bool{true, true, true, true}
			want[c.idx] = false
			if got := metOf(p); got != want {
				t.Fatalf("met = %v, want %v: %+v", got, want, p.Cutover.Criteria)
			}
			if len(p.Cutover.FollowUps) != 1 || !strings.Contains(p.Cutover.FollowUps[0].Title, p.Cutover.Criteria[c.idx].Name) {
				t.Fatalf("want exactly one follow-up naming the criterion: %+v", p.Cutover.FollowUps)
			}
			body := p.Cutover.FollowUps[0].Body
			for _, part := range []string{"Criterion: " + p.Cutover.Criteria[c.idx].Name, "Measured: " + p.Cutover.Criteria[c.idx].Have, "Gap: "} {
				if !strings.Contains(body, part) {
					t.Errorf("follow-up body lacks %q:\n%s", part, body)
				}
			}
			md := Markdown(Report{Phases: []PhaseReport{p}})
			if n := strings.Count(md, "\n- NOT MET: "); n != 1 {
				t.Errorf("want exactly one NOT MET line, got %d:\n%s", n, md)
			}
			if !strings.Contains(md, "### Follow-ups") {
				t.Errorf("an unmet criterion needs the follow-ups heading")
			}
		})
	}
}

func TestCutoverUnevaluableCriteriaAreNotMetWithTheirReason(t *testing.T) {
	// No live events at all: criteria 1 and 2 cannot be evaluated.
	p := cutoverFromDir(t, SynthOptions{Seeded: true}, func(in *Input) { in.Events = nil })
	if c := crit(p, CutoverMisses); c.Met || !strings.Contains(c.Evidence, "no LIVE-DETECTED events") || c.Have != "not evaluable" {
		t.Errorf("misses: %+v", c)
	}
	if c := crit(p, CutoverDelay); c.Met || !strings.Contains(c.Evidence, "cannot be evaluated") || !strings.Contains(c.Evidence, "n=0") {
		t.Errorf("delay: %+v", c)
	}
	// An older scratch directory: no cap in the manifest. Not defaulted.
	p = cutoverFromDir(t, SynthOptions{Seeded: true, NoDaemonCap: true}, cleanRun)
	if c := crit(p, CutoverCost); c.Met || c.Have != "cap unknown" || !strings.Contains(c.Evidence, "cap unknown") {
		t.Errorf("cost with no cap: %+v", c)
	}
	// No ticks at all: no slots.
	p = cutoverFromDir(t, SynthOptions{Seeded: true}, func(in *Input) { in.Rows = nil })
	if c := crit(p, CutoverUptime); c.Met || c.Have != "no slots" || !strings.Contains(c.Evidence, "no slots") {
		t.Errorf("uptime with no slots: %+v", c)
	}
	if c := crit(p, CutoverCost); c.Met || !strings.Contains(c.Evidence, "no hourly cost samples") {
		t.Errorf("cost with no hours: %+v", c)
	}
	// A run that supervised no daemon: every slot has no sample, none is up.
	p = cutoverFromDir(t, SynthOptions{Seeded: true, NoDaemonSamples: true}, cleanRun)
	if c := crit(p, CutoverUptime); c.Met || !strings.Contains(c.Have, "0 of 179 slots up") {
		t.Errorf("uptime with no samples: %+v", c)
	}
}

func TestCutoverUptimeCountsASlotWithNoSampleAsNotUpAndDegradedAsUp(t *testing.T) {
	// The synthetic run has 179 grid slots; 7 of them (the sleep gap) have no
	// tick row at all, so they have no sample.
	p := cutoverFromDir(t, SynthOptions{Seeded: true}, cleanRun)
	if c := crit(p, CutoverUptime); !strings.Contains(c.Have, "172 of 179") {
		t.Fatalf("slots with no tick row must count as not up: %+v", c)
	}
	// Dropping the sample of 1 more slot: 171 of 179.
	p = cutoverFromDir(t, SynthOptions{Seeded: true}, func(in *Input) {
		cleanRun(in)
		for i := range in.Rows {
			if in.Rows[i].Kind == schema.KindTick && in.Rows[i].Daemon != nil {
				in.Rows[i].Daemon = nil
				break
			}
		}
	})
	if c := crit(p, CutoverUptime); !strings.Contains(c.Have, "171 of 179") {
		t.Errorf("a tick row with no sample must count as not up: %+v", c)
	}
	// A degraded daemon is still serving; a down one is not.
	p = cutoverFromDir(t, SynthOptions{Seeded: true}, func(in *Input) {
		cleanRun(in)
		n := 0
		for i := range in.Rows {
			if in.Rows[i].Kind == schema.KindTick && in.Rows[i].Daemon != nil {
				switch {
				case n < 20:
					in.Rows[i].Daemon.State = schema.DaemonDegraded
				case n < 22:
					in.Rows[i].Daemon.State = schema.DaemonDown
				}
				n++
			}
		}
	})
	if c := crit(p, CutoverUptime); !strings.Contains(c.Have, "170 of 179") {
		t.Errorf("degraded counts as up, down does not: %+v", c)
	}
}

func TestCutoverUptimeBoundaryIsIntegerArithmetic(t *testing.T) {
	cases := []struct {
		up, total int
		met       bool
	}{
		{95, 100, true}, {94, 100, false}, {19, 20, true}, {18, 20, false}, {1, 1, true}, {0, 1, false},
	}
	for _, c := range cases {
		got := evalCutover(cutoverInput{upSlots: c.up, totalSlots: c.total}).Criteria[3]
		if got.Met != c.met {
			t.Errorf("%d of %d: met %v, want %v (%s)", c.up, c.total, got.Met, c.met, got.Evidence)
		}
	}
}

func hoursOf(costs ...int) []Hour {
	var h []Hour
	for i, c := range costs {
		h = append(h, Hour{Hour: fmt.Sprintf("2026-01-05T%02d:00Z", i), GraphQLCost: c})
	}
	return h
}

func repeat(v, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestRecommendationVerdictsAtTheirThresholds(t *testing.T) {
	cases := []struct {
		name    string
		cap     int
		p90     int
		verdict string
		prints  []string
	}{
		{"keep at the 1350 boundary", 1500, 1350, VerdictKeepCap, []string{"1500 x 0.9 = 1350", "p90 1350 <= 1350", "keep the cap"}},
		{"keep well under", 1500, 100, VerdictKeepCap, []string{"p90 100 <= 1350"}},
		{"raise just over 1350", 1500, 1351, "raise-cap-to-1600", []string{"p90 1351 > 1350", "1600 x 0.9 = 1440 >= 1351", "raise the cap to 1600"}},
		{"raise at the 1440 boundary", 1500, 1440, "raise-cap-to-1600", []string{"1600 x 0.9 = 1440 >= 1440"}},
		{"relax just over 1440", 1500, 1441, VerdictRelax, []string{"p90 1441 > 1350", "only cap step considered is 1600", "relax both detail_max_age and conversation_max_age to 2 h"}},
		{"relax far over at the default cap", 1500, 1900, VerdictRelax, nil},
		{"raised cap keeps at its own threshold", 1600, 1440, VerdictKeepCap, []string{"Cap C = 1600", "1600 x 0.9 = 1440", "p90 1440 <= 1440"}},
		{"raised cap steps to the smallest covering cap", 1600, 1441, "raise-cap-to-1700", []string{"1700 x 0.9 = 1530 >= 1441", "raise the cap to 1700"}},
		{"raised cap steps further", 1600, 1531, "raise-cap-to-1800", []string{"1800 x 0.9 = 1620 >= 1531"}},
		{"cap step reaches the ceiling", 3900, 3600, "raise-cap-to-4000", []string{"4000 x 0.9 = 3600 >= 3600"}},
		{"no step up to 4000 covers it", 3900, 3601, VerdictRelax, []string{"No cap step up to 4000 covers it"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Ten identical hourly samples make the nearest-rank p90 that value.
			r := recommend(hoursOf(repeat(c.p90, 10)...), c.cap)
			if r.Verdict != c.verdict {
				t.Fatalf("verdict %q, want %q\n%s", r.Verdict, c.verdict, r.Method)
			}
			for _, want := range append([]string{"n=10", "p90 = " + fmt.Sprint(c.p90), "maximum = " + fmt.Sprint(c.p90), "background share = 0.9"}, c.prints...) {
				if !strings.Contains(r.Method, want) {
					t.Errorf("method lacks %q:\n%s", want, r.Method)
				}
			}
		})
	}
}

func TestRecommendationUsesEveryHourIncludingZeroCostOnes(t *testing.T) {
	// 8 zero-cost hours and two of 1500: nearest-rank p90 of 10 samples is the
	// 9th smallest, 1500 (over 1,440), so the verdict relaxes; the zeros are in.
	r := recommend(hoursOf(0, 0, 0, 0, 0, 0, 0, 0, 1500, 1500), 1500)
	if r.Verdict != VerdictRelax || !strings.Contains(r.Method, "[0 0 0 0 0 0 0 0 1500 1500]") || !strings.Contains(r.Method, "p90 = 1500") || !strings.Contains(r.Method, "maximum = 1500") {
		t.Errorf("%+v", r)
	}
	// One high hour among nine zeros: p90 is the 9th smallest, 0: keep the cap
	// although the maximum is 5000.
	r = recommend(hoursOf(0, 0, 0, 0, 0, 0, 0, 0, 0, 5000), 1500)
	if r.Verdict != VerdictKeepCap || !strings.Contains(r.Method, "maximum = 5000") {
		t.Errorf("%+v", r)
	}
}

func TestRecommendationWithoutSamplesOrCap(t *testing.T) {
	r := recommend(nil, 1500)
	if r.Verdict != VerdictKeepCap || !strings.Contains(r.Method, "No hourly cost samples") {
		t.Errorf("%+v", r)
	}
	r = recommend(hoursOf(repeat(1400, 10)...), 0)
	if r.Verdict != "raise-cap-to-1600" || !strings.Contains(r.Method, "1,500 default is assumed") {
		t.Errorf("an unknown cap assumes the default and says so: %+v", r)
	}
}

func TestCutoverMarkdownAndJSONAgreeOnEveryVerdict(t *testing.T) {
	dir := t.TempDir()
	if _, err := Synthesize(dir, SynthOptions{Seeded: true, CIChanges: true}); err != nil {
		t.Fatal(err)
	}
	rep, err := Build([]string{dir}, params())
	if err != nil {
		t.Fatal(err)
	}
	jp, mp, err := Write(rep, filepath.Join(dir, "reports"))
	if err != nil {
		t.Fatal(err)
	}
	jb, _ := os.ReadFile(jp)
	mb, _ := os.ReadFile(mp)
	var doc struct {
		Phases []struct {
			Cutover struct {
				Criteria []struct {
					Name     string `json:"name"`
					Need     string `json:"need"`
					Have     string `json:"have"`
					Met      *bool  `json:"met"`
					Evidence string `json:"evidence"`
				} `json:"criteria"`
				Recommendation struct {
					Verdict string `json:"verdict"`
					Method  string `json:"method"`
				} `json:"recommendation"`
				FollowUps []struct {
					Title string `json:"title"`
					Body  string `json:"body"`
				} `json:"follow_ups"`
			} `json:"cutover"`
		} `json:"phases"`
	}
	if err := json.Unmarshal(jb, &doc); err != nil {
		t.Fatal(err)
	}
	c := doc.Phases[0].Cutover
	if len(c.Criteria) != 4 {
		t.Fatalf("json criteria: %+v", c.Criteria)
	}
	md := string(mb)
	if !strings.Contains(md, "\n## Cutover criteria\n") || !strings.Contains(md, "\n### Budget recommendation\n") {
		t.Fatalf("markdown headings:\n%s", md)
	}
	lines := cutoverLines(md)
	if len(lines) != 4 {
		t.Fatalf("markdown verdict lines: %q", lines)
	}
	unmet := 0
	for i, cr := range c.Criteria {
		if cr.Met == nil || cr.Need == "" || cr.Have == "" || cr.Evidence == "" {
			t.Errorf("json criterion %d is missing a field: %+v", i, cr)
		}
		word := "MET"
		if !*cr.Met {
			word = "NOT MET"
			unmet++
		}
		if want := "- " + word + ": " + cr.Name + ": " + cr.Have; lines[i] != want {
			t.Errorf("markdown line %d = %q, json says %q", i, lines[i], want)
		}
	}
	if !strings.Contains(md, "Recommendation: "+c.Recommendation.Verdict+"\n") || !strings.Contains(md, c.Recommendation.Method) {
		t.Errorf("recommendation differs between markdown and json: %q", c.Recommendation.Verdict)
	}
	if len(c.FollowUps) != unmet {
		t.Errorf("follow_ups %d, unmet %d", len(c.FollowUps), unmet)
	}
	for _, f := range c.FollowUps {
		if !strings.Contains(md, "- "+f.Title+"\n") {
			t.Errorf("follow-up %q missing from markdown", f.Title)
		}
	}
	if unmet == 0 {
		t.Fatal("the selftest scenario is expected to have unmet criteria")
	}
}

// cutoverLines returns the verdict lines under the ## Cutover criteria heading.
func cutoverLines(md string) []string {
	var out []string
	in := false
	for _, l := range strings.Split(md, "\n") {
		switch {
		case l == "## Cutover criteria":
			in = true
		case strings.HasPrefix(l, "#"):
			in = false
		case in && (strings.HasPrefix(l, "- MET: ") || strings.HasPrefix(l, "- NOT MET: ")):
			out = append(out, l)
		}
	}
	return out
}

func TestCombineCarriesPhaseCutoverVerdictsUnchanged(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	if _, err := Synthesize(dirA, SynthOptions{Seeded: true, Phase: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Synthesize(dirB, SynthOptions{Phase: "B", NoDaemonCap: true}); err != nil {
		t.Fatal(err)
	}
	rep, err := Build([]string{dirA, dirB}, params())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Combined == nil || len(rep.Combined.Cutover) != 8 {
		t.Fatalf("combined cutover: %+v", rep.Combined)
	}
	i := 0
	for _, p := range rep.Phases {
		for _, cr := range p.Cutover.Criteria {
			if e := rep.Combined.Cutover[i]; e.Phase != p.Phase || e.Name != cr.Name || e.Met != cr.Met {
				t.Errorf("combined entry %d = %+v, phase says %s %s %v", i, e, p.Phase, cr.Name, cr.Met)
			}
			i++
		}
	}
	md := Markdown(rep)
	idx := strings.Index(md, "## Combined cutover criteria")
	if idx < 0 {
		t.Fatalf("no combined cutover heading:\n%s", md)
	}
	tail := md[idx:]
	if n := strings.Count(tail, "\n- A: ") + strings.Count(tail, "\n- B: "); n != 8 {
		t.Errorf("combined lines = %d:\n%s", n, tail)
	}
	if !strings.Contains(tail, "- B: NOT MET: "+CutoverCost) {
		t.Errorf("phase B has no cap, so its cost criterion is NOT MET:\n%s", tail)
	}
}

func TestSelfTestExercisesTheCutoverSection(t *testing.T) {
	var out strings.Builder
	if err := SelfTest(&out); err != nil {
		t.Fatal(err)
	}
}

func TestCutoverDelayAndCostBoundaries(t *testing.T) {
	d := func(p90 float64) Dist { return Dist{N: 5, P90: p90} }
	for _, c := range []struct {
		shadow, live float64
		met          bool
	}{{60, 60, true}, {59.9, 60, true}, {60.1, 60, false}} {
		got := evalCutover(cutoverInput{shadowDelay: d(c.shadow), liveDelay: d(c.live)}).Criteria[1]
		if got.Met != c.met {
			t.Errorf("shadow %.1f live %.1f: met %v (%s)", c.shadow, c.live, got.Met, got.Evidence)
		}
	}
	for _, c := range []struct {
		cost int
		met  bool
	}{{1500, true}, {1499, true}, {1501, false}} {
		got := evalCutover(cutoverInput{cap: 1500, hours: hoursOf(0, c.cost, 3)}).Criteria[2]
		if got.Met != c.met {
			t.Errorf("hour cost %d: met %v (%s)", c.cost, got.Met, got.Evidence)
		}
	}
	// The cap of the run, not the 1,500 default, bounds the hours.
	if got := evalCutover(cutoverInput{cap: 1600, hours: hoursOf(1600)}).Criteria[2]; !got.Met {
		t.Errorf("a raised cap of 1600 allows an hour of 1600: %+v", got)
	}
}
