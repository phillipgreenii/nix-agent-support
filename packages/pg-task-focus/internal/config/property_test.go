package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
)

var (
	zoneNames  = []string{"America/New_York", "UTC", "Europe/Paris", "Asia/Tokyo", "EST5EDT", "Australia/Lord_Howe", "Pacific/Apia"}
	groupNames = []string{"Start of day", "During the day", "End of day", "Extras"}
	soundNames = []string{"Glass", "Tink", "Hero", "Ping"}
	keyNames   = []string{"pr", "ticket", "incident", "a-b_9", "0"}
	weekdays   = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	urls       = []string{"http://example.test", "https://focus.example.test:8443/base", "http://127.0.0.1:49210"}
)

func idGen() *rapid.Generator[string] { return rapid.StringMatching(`[a-z][a-z0-9-]{0,6}`) }

func minutesGen() *rapid.Generator[int] { return rapid.IntRange(1, 525600) }

// subset draws a distinct subset of ids, in a drawn order.
func subset(t *rapid.T, label string, ids []string) []any {
	if len(ids) == 0 {
		return []any{}
	}
	picked := rapid.SliceOfNDistinct(rapid.SampledFrom(ids), 0, len(ids), func(s string) string { return s }).Draw(t, label)
	out := make([]any, len(picked))
	for i, s := range picked {
		out[i] = s
	}
	return out
}

func optional(t *rapid.T, label string, set func()) {
	if rapid.Bool().Draw(t, label) {
		set()
	}
}

// validConfigGen draws a configuration that satisfies every rule: the loader
// MUST accept all of them.
func validConfigGen() *rapid.Generator[map[string]any] {
	return rapid.Custom(func(t *rapid.T) map[string]any {
		taskIDs := rapid.SliceOfNDistinct(idGen(), 0, 6, func(s string) string { return s }).Draw(t, "task ids")
		cycleIDs := rapid.SliceOfNDistinct(idGen(), 0, 4, func(s string) string { return s }).Draw(t, "cycle ids")
		profileNames := rapid.SliceOfNDistinct(idGen(), 1, 3, func(s string) string { return s }).Draw(t, "profile names")

		tasks := map[string]any{}
		cadenceOf := map[string]string{}
		for _, id := range taskIDs {
			cadence := rapid.SampledFrom([]string{"daily", "weekly", "sprint"}).Draw(t, "cadence")
			cadenceOf[id] = cadence
			due := map[string]any{
				"at": fmt.Sprintf("%02d:%02d", rapid.IntRange(0, 23).Draw(t, "hour"), rapid.IntRange(0, 59).Draw(t, "minute")),
				"tz": rapid.SampledFrom(zoneNames).Draw(t, "tz"),
			}
			switch cadence {
			case "weekly":
				due["weekday"] = rapid.SampledFrom(weekdays).Draw(t, "weekday")
			case "sprint":
				due["day"] = rapid.IntRange(1, 40).Draw(t, "day")
			}
			task := map[string]any{"title": rapid.StringMatching(`[A-Za-z ]{1,20}`).Draw(t, "title"), "cadence": cadence, "due": due}
			optional(t, "has group", func() { task["group"] = rapid.SampledFrom(groupNames).Draw(t, "group") })
			optional(t, "has link", func() { task["link"] = "https://tasks.example.test/" + id })
			tasks[id] = task
		}

		cycles := map[string]any{}
		for _, id := range cycleIDs {
			cy := map[string]any{"title": rapid.StringMatching(`[A-Za-z ]{1,20}`).Draw(t, "cycle title")}
			optional(t, "has minutes", func() { cy["minutes"] = minutesGen().Draw(t, "minutes") })
			optional(t, "has keys", func() { cy["keys"] = subset(t, "keys", keyNames) })
			optional(t, "has alert", func() {
				alert := map[string]any{}
				optional(t, "alert sound", func() { alert["sound"] = rapid.SampledFrom(soundNames).Draw(t, "sound") })
				optional(t, "alert reminder", func() { alert["reminder_sound"] = rapid.SampledFrom(soundNames).Draw(t, "reminder") })
				optional(t, "alert repeat", func() { alert["repeat_minutes"] = minutesGen().Draw(t, "repeat") })
				cy["alert"] = alert
			})
			cycles[id] = cy
		}

		profiles := map[string]any{}
		for _, name := range profileNames {
			p := map[string]any{}
			for _, cadence := range []string{"daily", "weekly", "sprint"} {
				var ids []string
				for _, id := range taskIDs {
					if cadenceOf[id] == cadence {
						ids = append(ids, id)
					}
				}
				optional(t, "lists "+cadence, func() { p[cadence] = subset(t, cadence, ids) })
			}
			optional(t, "lists cycles", func() { p["cycles"] = subset(t, "cycles", cycleIDs) })
			profiles[name] = p
		}

		alert := map[string]any{"sound": rapid.SampledFrom(soundNames).Draw(t, "default sound"), "repeat_minutes": minutesGen().Draw(t, "default repeat")}
		optional(t, "default reminder", func() { alert["reminder_sound"] = rapid.SampledFrom(soundNames).Draw(t, "default reminder") })
		defaults := map[string]any{
			"cycle_minutes": minutesGen().Draw(t, "cycle minutes"),
			"profile":       rapid.SampledFrom(profileNames).Draw(t, "default profile"),
			"alert":         alert,
		}
		optional(t, "has boosts", func() {
			defaults["boost_minutes"] = rapid.SliceOfN(minutesGen(), 0, 4).Draw(t, "boosts")
		})
		optional(t, "has skew", func() { defaults["max_future_skew_seconds"] = rapid.IntRange(0, 31536000).Draw(t, "skew") })
		optional(t, "has attention", func() {
			a := map[string]any{}
			for _, k := range []string{"due_soon_minutes", "overtime_high_minutes", "stale_pause_minutes"} {
				optional(t, k, func() { a[k] = minutesGen().Draw(t, k) })
			}
			defaults["attention"] = a
		})

		cfg := map[string]any{
			"defaults":    defaults,
			"listen_port": rapid.IntRange(1, 65535).Draw(t, "port"),
			"profiles":    profiles,
			"tasks":       tasks,
			"cycles":      cycles,
		}
		optional(t, "has public_url", func() { cfg["public_url"] = rapid.SampledFrom(urls).Draw(t, "public_url") })
		optional(t, "has group_order", func() { cfg["group_order"] = subset(t, "group order", groupNames) })
		return cfg
	})
}

// checkParseResult asserts what holds of every Parse result, and returns the
// Config of an accepted document.
func checkParseResult(t *rapid.T, raw []byte) *config.Config {
	c, err := config.Parse(raw)
	if err != nil {
		var ve *config.ValidationError
		if !errors.As(err, &ve) || len(ve.Problems) == 0 {
			t.Fatalf("Parse error = %v (%T), want a *ValidationError with problems", err, err)
		}
		if !sort.SliceIsSorted(ve.Problems, func(i, j int) bool { return ve.Problems[i].Path < ve.Problems[j].Path }) {
			t.Fatalf("problems are not in path order: %v", ve)
		}
		for _, p := range ve.Problems {
			if p.Message == "" {
				t.Fatalf("a Problem at %q has no message", p.Path)
			}
		}
		if c != nil {
			t.Fatal("Parse returned a Config alongside an error")
		}
		return nil
	}
	if c == nil {
		t.Fatal("Parse returned neither a Config nor an error")
	}
	return c
}

// remarshalled returns the generic re-encoding of raw: another spelling of the
// same document, with the keys sorted and no whitespace.
func remarshalled(t *rapid.T, raw []byte, indent bool) []byte {
	var tree any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		t.Fatalf("re-reading an accepted document: %v", err)
	}
	var out []byte
	var err error
	if indent {
		out, err = json.MarshalIndent(tree, "", "\t")
	} else {
		out, err = json.Marshal(tree)
	}
	if err != nil {
		t.Fatalf("re-marshalling an accepted document: %v", err)
	}
	return out
}

// checkRoundTrip asserts that an accepted document re-marshalled is accepted
// again with an equal digest.
func checkRoundTrip(t *rapid.T, raw []byte, c *config.Config) {
	for _, indent := range []bool{false, true} {
		again, err := config.Parse(remarshalled(t, raw, indent))
		if err != nil {
			t.Fatalf("the re-marshalled document (indent=%v) was rejected: %v\nbefore: %s", indent, err, raw)
		}
		if again.Digest() != c.Digest() {
			t.Fatalf("the digest changed across a re-marshal (indent=%v): %s vs %s", indent, again.Digest(), c.Digest())
		}
	}
}

// TestPropertyAcceptedConfigRoundTrips: a configuration that satisfies every
// rule is accepted, and what Parse accepts, re-marshalled, is accepted again
// with an equal Digest.
func TestPropertyAcceptedConfigRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		raw, err := json.Marshal(validConfigGen().Draw(t, "config"))
		if err != nil {
			t.Fatal(err)
		}
		c, err := config.Parse(raw)
		if err != nil {
			t.Fatalf("a configuration that satisfies every rule was rejected: %v\n%s", err, raw)
		}
		checkRoundTrip(t, raw, c)

		// The parsed values agree with the document: the profile the document
		// names is defined, and resolution never returns a zero.
		d := c.Defaults()
		if _, ok := c.Profile(d.Profile); !ok {
			t.Fatalf("Defaults().Profile %q is not defined", d.Profile)
		}
		if a := c.Alert("any-type"); a.Sound == "" || a.ReminderSound == "" || a.RepeatMinutes < 1 {
			t.Fatalf("Alert = %+v", a)
		}
		if c.CycleMinutes("any-type", nil) < 1 {
			t.Fatal("CycleMinutes < 1")
		}
	})
}

// JSON values a mutation puts in place of a value of a valid configuration.
var replacements = []any{nil, true, false, 0, -1, 1, 525601, 1.5, json.Number("1e400"), json.Number("99999999999999999999"), "", "x", "ET", "Local", "cycle_type", "ftp://x", []any{}, []any{"a"}, map[string]any{}, map[string]any{"a": 1}}

// paths lists the pointers (as token lists) of every value of a tree.
func treePaths(v any, prefix []string, out *[][]string) {
	*out = append(*out, append([]string(nil), prefix...))
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			treePaths(x[k], append(prefix, k), out)
		}
	case []any:
		for i, e := range x {
			treePaths(e, append(prefix, fmt.Sprint(i)), out)
		}
	}
}

// mutate returns tree with the value at path replaced, or removed when remove.
func mutate(tree any, path []string, repl any, remove bool) any {
	if len(path) == 0 {
		return repl
	}
	switch x := tree.(type) {
	case map[string]any:
		if len(path) == 1 && remove {
			delete(x, path[0])
			return x
		}
		x[path[0]] = mutate(x[path[0]], path[1:], repl, remove)
	case []any:
		var i int
		if _, err := fmt.Sscan(path[0], &i); err == nil && i >= 0 && i < len(x) {
			if len(path) == 1 && remove {
				return slices.Delete(x, i, i+1)
			}
			x[i] = mutate(x[i], path[1:], repl, remove)
		}
	}
	return tree
}

// TestPropertyMutatedConfigNeverPanics: a valid configuration with one value
// replaced or removed, and with random bytes changed, is either accepted (and
// then round-trips) or rejected with problems; Parse never panics.
func TestPropertyMutatedConfigNeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var tree any
		raw, err := json.Marshal(validConfigGen().Draw(t, "config"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &tree); err != nil {
			t.Fatal(err)
		}
		var all [][]string
		treePaths(tree, nil, &all)
		path := rapid.SampledFrom(all).Draw(t, "path")
		mutated, err := json.Marshal(mutate(tree, path, rapid.SampledFrom(replacements).Draw(t, "replacement"), rapid.Bool().Draw(t, "remove")))
		if err != nil {
			t.Skip("the mutation is not encodable")
		}
		if c := checkParseResult(t, mutated); c != nil {
			checkRoundTrip(t, mutated, c)
		}

		// Then damage the bytes of the document itself.
		damaged := slices.Clone(mutated)
		for range rapid.IntRange(1, 4).Draw(t, "damage") {
			if len(damaged) == 0 {
				break
			}
			i := rapid.IntRange(0, len(damaged)-1).Draw(t, "index")
			switch rapid.IntRange(0, 2).Draw(t, "kind") {
			case 0:
				damaged[i] = rapid.Byte().Draw(t, "byte")
			case 1:
				damaged = slices.Delete(damaged, i, i+1)
			default:
				damaged = slices.Insert(damaged, i, rapid.Byte().Draw(t, "byte"))
			}
		}
		if c := checkParseResult(t, damaged); c != nil {
			checkRoundTrip(t, damaged, c)
		}
	})
}

// TestPropertyParseNeverPanicsOnArbitraryBytes: Parse of any bytes returns a
// Config or a *ValidationError.
func TestPropertyParseNeverPanicsOnArbitraryBytes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		raw := rapid.SliceOf(rapid.Byte()).Draw(t, "bytes")
		if c := checkParseResult(t, raw); c != nil {
			checkRoundTrip(t, raw, c)
		}
	})
}
