package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/schemas"
)

// `pg-router doctor --clauses`: the read-only report of what the CURRENT readings
// would pause.
//
// Exported names: none (package main). Names: doctorClauses, sensorReadings,
// bandRow, bandTable, shortfall.
//
// It runs every level-source query of the config once with --raw (the reading
// with no state, no dwell, no hysteresis) and once with --bands (the sensor's own
// band table, so "needs >= 25 GiB" and the shortfall come from the sensor), with
// no dependence on a running router or its projection, so it works before the
// router enforces. Each registration is judged by levels.Decide, the function the
// router admits with, over those raw readings. A sensor that fails or prints no
// answer for a resource leaves it at "no reading" and the exit status stays 0.

// bandRow is one band of a --bands line: min inclusive, max exclusive, either
// null (open).
type bandRow struct {
	Level string   `json:"level"`
	Min   *float64 `json:"min"`
	Max   *float64 `json:"max"`
}

// bandTable is one resource's --bands line.
type bandTable struct {
	Resource string    `json:"resource"`
	Unit     string    `json:"unit"`
	Bands    []bandRow `json:"bands"`
}

// rawAnswer is one level answer of a --raw run.
type rawAnswer struct {
	Resource string       `json:"resource"`
	Level    string       `json:"level"`
	Reading  *readingView `json:"reading"`
	Owner    string       `json:"owner"`
}

// sensorReadings is everything one sensor told doctor.
type sensorReadings struct {
	sensor  string
	answers map[string]rawAnswer
	bands   map[string]bandTable
	notes   []string // why the sensor answered nothing or something unusable
}

// withArgs returns a copy of the command query q with extra appended to its argv.
func withArgs(q query.Query, extra ...string) (query.Query, error) {
	var cq query.CommandQuery
	switch v := q.(type) {
	case query.CommandQuery:
		cq = v
	case *query.CommandQuery:
		cq = *v
	default:
		return nil, fmt.Errorf("a level source must be a command query, got %T", q)
	}
	cq.Argv = append(slices.Clone(cq.Argv), extra...)
	return cq, nil
}

// readSensor runs one level source with --raw and with --bands, each once.
func readSensor(ctx context.Context, env query.Env, s query.Source, resources config.ResourceSet) sensorReadings {
	out := sensorReadings{sensor: s.LevelSource, answers: map[string]rawAnswer{}, bands: map[string]bandTable{}}
	deadline := s.Deadline
	if deadline <= 0 {
		deadline = query.DefaultDeadline
	}
	run := func(flag string) ([]byte, error) {
		q, err := withArgs(s.Query, flag)
		if err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()
		return query.RunRaw(cctx, env, q, nil)
	}

	raw, err := run("--raw")
	if err != nil {
		out.notes = append(out.notes, "--raw failed: "+err.Error())
	} else {
		sc := bufio.NewScanner(bytes.NewReader(raw))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lines := 0
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			lines++
			if verr := schemas.ValidateLevelAnswer(line); verr != nil {
				out.notes = append(out.notes, "malformed answer ignored: "+verr.Error())
				continue
			}
			var a rawAnswer
			if json.Unmarshal(line, &a) != nil {
				continue
			}
			res, ok := resources.Find(a.Resource)
			if !ok || !slices.Contains(res.Scale, a.Level) {
				out.notes = append(out.notes, fmt.Sprintf("answer for %q at level %q is not in the registry; ignored", a.Resource, a.Level))
				continue
			}
			out.answers[a.Resource] = a
		}
		if lines == 0 {
			out.notes = append(out.notes, "--raw printed no level answer")
		}
	}

	if braw, berr := run("--bands"); berr == nil {
		sc := bufio.NewScanner(bytes.NewReader(braw))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			var bt bandTable
			if len(line) > 0 && json.Unmarshal(line, &bt) == nil && bt.Resource != "" {
				out.bands[bt.Resource] = bt
			}
		}
	}
	return out
}

// shortfall says what a failing clause needs and how far the reading is from it,
// from the sensor's band table: needs is ">= 25 GiB" (or "< 8 load"), short is
// "5.1 GiB". ok is false without numeric bands for the allowed levels (a
// two-level scale, or a sensor that printed no table).
func shortfall(value float64, bt bandTable, allowed []string) (needs, short string, ok bool) {
	best := -1.0
	for _, b := range bt.Bands {
		if !slices.Contains(allowed, b.Level) {
			continue
		}
		var dist float64
		var n string
		switch {
		case b.Min != nil && value < *b.Min:
			dist, n = *b.Min-value, ">= "+formatNumber(*b.Min)
		case b.Max != nil && value >= *b.Max:
			dist, n = value-*b.Max, "< "+formatNumber(*b.Max)
		default:
			continue
		}
		if best < 0 || dist < best {
			best, needs = dist, n
		}
	}
	if best < 0 {
		return "", "", false
	}
	unit := ""
	if bt.Unit != "" {
		unit = " " + textsafe.Sanitize(bt.Unit)
	}
	return needs + unit, formatNumber(best) + unit, true
}

// clauseDetail is one failing clause as doctor prints it.
func clauseDetail(e levels.Entry, ans map[string]rawAnswer, bands map[string]bandTable) string {
	head := fmt.Sprintf("%s: %s is %s", textsafe.Sanitize(e.ClauseText), textsafe.Sanitize(e.Resource), textsafe.Sanitize(e.Level))
	if e.Kind == levels.KindUnknown {
		return head + " (no reading; a silent resource reads " + textsafe.Sanitize(e.Level) + ")"
	}
	a := ans[e.Resource]
	if a.Reading == nil {
		return head
	}
	detail := a.Reading.String()
	if needs, short, ok := shortfall(a.Reading.Value, bands[e.Resource], e.Allowed); ok {
		detail += "; needs " + needs + "; " + short + " short"
	}
	return head + " (" + detail + ")"
}

// doctorClauses prints the --clauses report for cfg and returns the exit status:
// 0 whenever the config loaded, whatever the sensors said.
func doctorClauses(w io.Writer, cfg config.Config, env query.Env) int {
	var sources query.SourceSet
	for _, s := range cfg.Queries {
		if s.LevelSource != "" && s.Query != nil {
			sources = append(sources, s)
		}
	}
	reads := make([]sensorReadings, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reads[i] = readSensor(context.Background(), env, s, cfg.Resources)
		}()
	}
	wg.Wait()

	answers := map[string]rawAnswer{}
	bands := map[string]bandTable{}
	for _, r := range reads {
		for k, v := range r.answers {
			if res, ok := cfg.Resources.Find(k); ok && res.Sensor == r.sensor {
				answers[k] = v
			}
		}
		for k, v := range r.bands {
			bands[k] = v
		}
	}

	now := time.Now()
	snap := levels.Snapshot{Now: now, Levels: map[string]levels.Level{}}
	for name, a := range answers {
		snap.Levels[name] = levels.Level{Value: a.Level, State: levels.StateHeld, Source: "sensor", Owner: a.Owner, Since: now}
	}
	reg := cfg.Resources.Registry()

	fmt.Fprintln(w, "doctor --clauses: read-only; each sensor was run once with --raw (no state, no dwell, no hysteresis)")
	for _, r := range reads {
		for _, n := range r.notes {
			fmt.Fprintf(w, "  sensor %s: %s\n", textsafe.Sanitize(r.sensor), textsafe.Sanitize(n))
		}
	}

	fmt.Fprintln(w, "registrations:")
	total, blocked := 0, 0
	for _, role := range cfg.Roles {
		if !role.Enabled {
			continue
		}
		total++
		res := levels.Decide(snap, reg, cfg.Requirements[role.Name].Requirement)
		name := textsafe.Sanitize(role.Name)
		if res.Admit {
			fmt.Fprintf(w, "  %s  ok\n", name)
			continue
		}
		blocked++
		parts := make([]string, 0, len(res.Failing))
		for _, e := range res.Failing {
			parts = append(parts, clauseDetail(e, answers, bands))
		}
		fmt.Fprintf(w, "  %s  BLOCKED NOW  %s\n", name, strings.Join(parts, "; "))
	}
	fmt.Fprintf(w, "%d of %d registrations blocked\n", blocked, total)

	fmt.Fprintln(w, "levels:")
	var below []string
	for _, r := range cfg.Resources {
		name := textsafe.Sanitize(r.Name)
		a, has := answers[r.Name]
		switch {
		case has:
			line := fmt.Sprintf("  %s  %s", name, textsafe.Sanitize(a.Level))
			if a.Reading != nil {
				line += "  " + a.Reading.String()
			}
			fmt.Fprintf(w, "%s  (sensor %s)\n", line, textsafe.Sanitize(a.Owner))
			ok := slices.Index(r.Scale, "OK")
			if ok >= 0 && slices.Index(r.Scale, a.Level) > ok {
				below = append(below, textsafe.Sanitize(r.Name)+" "+textsafe.Sanitize(a.Level))
			}
		case r.Sensor == config.RouterSensor:
			fmt.Fprintf(w, "  %s  %s  (the router's own resource; its pause is not read here)\n", name, textsafe.Sanitize(r.UnknownLevel()))
		case !cfg.SensorPresent(r.Sensor):
			fmt.Fprintf(w, "  %s  no reading  (no sensor configured)\n", name)
		default:
			fmt.Fprintf(w, "  %s  no reading  (sensor has no answer)\n", name)
		}
	}
	if len(below) > 0 {
		fmt.Fprintf(w, "note: the bands start this machine below OK on %s; a registration that requires OK or better on them does not start until they recover\n", strings.Join(below, ", "))
	} else {
		fmt.Fprintln(w, "note: no resource with a reading starts below OK")
	}
	return exitOK
}
