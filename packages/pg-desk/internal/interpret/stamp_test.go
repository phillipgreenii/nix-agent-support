package interpret

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// Tests of the interpretation stamp: INV-REINTERPRET-4 (the stamp is the build
// version plus a digest of exactly the configuration interpret reads) and
// INV-REINTERPRET-5 (the digest is unchanged by the discovery path, key order
// and whitespace, and changes when a value interpret reads changes).

func fullConfig() *config.Config {
	return &config.Config{
		Path:               "/home/a/config.yaml",
		SelfLogin:          "me",
		SelfIssueOwner:     "owner",
		TeamMembers:        []string{"a", "b"},
		WatchLabels:        []string{"w"},
		ApproverAllowlist:  []string{"app"},
		VerdictGenerations: []config.VerdictGeneration{{ID: "g1", BodyMarker: "m"}},
		CheckInterpreters:  []config.CheckInterpreterConfig{{Type: "t", Patterns: []string{"p"}}},
		ReviewExemptChecks: []string{"job"},
		Jira:               &config.JiraConfig{HighPriorityValues: []string{"High"}, DoneStatuses: []string{"Done"}},
		CategoryVocabulary: map[string][]string{"bug": {"fix"}, "feat": {"add"}},
		Urgency:            &config.UrgencyConfig{Labels: []string{"urgent"}, Thresholds: map[string]int{"high": 5}},
		Serve:              config.ServeConfig{Addr: "127.0.0.1:1"},
	}
}

func TestStamp_Format(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}$`)
	if d := ConfigDigest(fullConfig()); !re.MatchString(d) {
		t.Fatalf("digest %q is not 8 hex characters", d)
	}
	cfg := fullConfig()
	if got, want := Stamp("0.0.0-1a2b3c4d", cfg), "0.0.0-1a2b3c4d/"+ConfigDigest(cfg); got != want {
		t.Fatalf("Stamp = %q, want %q", got, want)
	}
	if got := Stamp("dev", cfg); !strings.HasPrefix(got, "dev/") {
		t.Fatalf("dev build stamp = %q, want dev/<digest>", got)
	}
	// A nil configuration is the zero configuration: it stamps, and it
	// differs from a configured one.
	if Stamp("v", nil) == Stamp("v", cfg) {
		t.Fatal("a nil config and a configured one stamp alike")
	}
	if Stamp("v", nil) != Stamp("v", &config.Config{}) {
		t.Fatal("a nil config and an empty one must stamp alike")
	}
	if Stamp("v1", cfg) == Stamp("v2", cfg) {
		t.Fatal("the version is not in the stamp")
	}
}

// INV-REINTERPRET-5: the discovery path and every field interpret does not
// read leave the digest unchanged.
func TestConfigDigest_IgnoresWhatInterpretDoesNotRead(t *testing.T) {
	base := ConfigDigest(fullConfig())
	for name, mutate := range map[string]func(*config.Config){
		"path":       func(c *config.Config) { c.Path = "/elsewhere/config.yaml" },
		"serve":      func(c *config.Config) { c.Serve.Addr = "0.0.0.0:9" },
		"heartbeat":  func(c *config.Config) { c.HeartbeatPeriod = "5m" },
		"stale":      func(c *config.Config) { c.StaleAfter = "1h" },
		"actor":      func(c *config.Config) { c.Actor = "someone" },
		"ticket":     func(c *config.Config) { c.TicketPatterns = []string{"X-\\d+"} },
		"ci attempt": func(c *config.Config) { c.CIOnlyAttemptsThreshold = 9 },
		"nil vs empty list": func(c *config.Config) {
			c.WatchLabels = append([]string(nil), c.WatchLabels...)
			c.ApproverAllowlist = append([]string{}, c.ApproverAllowlist...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := fullConfig()
			mutate(c)
			if got := ConfigDigest(c); got != base {
				t.Fatalf("digest changed (%s -> %s) for a field interpret does not read", base, got)
			}
		})
	}
}

// INV-REINTERPRET-5: a change to any value interpret reads changes the digest.
func TestConfigDigest_ChangesWithEveryFieldInterpretReads(t *testing.T) {
	base := ConfigDigest(fullConfig())
	for name, mutate := range map[string]func(*config.Config){
		"self_login":          func(c *config.Config) { c.SelfLogin = "you" },
		"self_issue_owner":    func(c *config.Config) { c.SelfIssueOwner = "x" },
		"team_members":        func(c *config.Config) { c.TeamMembers = []string{"a", "b", "c"} },
		"watch_labels":        func(c *config.Config) { c.WatchLabels = nil },
		"approver_allowlist":  func(c *config.Config) { c.ApproverAllowlist = []string{"z"} },
		"verdict_generations": func(c *config.Config) { c.VerdictGenerations[0].BodyMarker = "other" },
		"check_interpreters":  func(c *config.Config) { c.CheckInterpreters[0].Type = "u" },
		"review_exempt":       func(c *config.Config) { c.ReviewExemptChecks = []string{"job", "job2"} },
		"jira":                func(c *config.Config) { c.Jira.HighPriorityValues = []string{"Highest"} },
		"jira removed":        func(c *config.Config) { c.Jira = nil },
		"category_vocabulary": func(c *config.Config) { c.CategoryVocabulary["bug"] = []string{"fix", "patch"} },
		"urgency":             func(c *config.Config) { c.Urgency.Thresholds["high"] = 6 },
		"in_progress default": func(c *config.Config) { c.Jira.InProgressStatuses = []string{"Doing"} },
	} {
		t.Run(name, func(t *testing.T) {
			c := fullConfig()
			mutate(c)
			if got := ConfigDigest(c); got == base {
				t.Fatalf("digest did not change when %s changed", name)
			}
		})
	}
}

// A reordered or re-spaced configuration file digests the same.
func TestConfigDigest_UnchangedByKeyOrderAndWhitespace(t *testing.T) {
	a := "self_login: me\nteam_members: [a, b]\nurgency:\n  labels: [u]\n  thresholds: {high: 5, low: 1}\ncategory_vocabulary:\n  bug: [fix]\n  feat: [add]\n"
	b := "category_vocabulary: {feat: [add], bug: [fix]}\n\n\nurgency: {thresholds: {low: 1, high: 5}, labels: [u]}\nteam_members:\n    - a\n    - b\nself_login:    me\n"
	parse := func(doc string) *config.Config {
		var c config.Config
		if err := yaml.Unmarshal([]byte(doc), &c); err != nil {
			t.Fatal(err)
		}
		return &c
	}
	if da, db := ConfigDigest(parse(a)), ConfigDigest(parse(b)); da != db {
		t.Fatalf("reordered or re-spaced config digests differ: %s vs %s", da, db)
	}
}

// FuzzStamp: whatever configuration the bytes parse to, re-encoding it with
// reordered keys and different whitespace (through a JSON map, whose keys
// marshal sorted) gives the same digest. The seed corpus runs as ordinary
// tests; a longer fuzz run is a manual step.
func FuzzStamp(f *testing.F) {
	f.Add([]byte("self_login: me\nteam_members: [a, b]\n"))
	f.Add([]byte("urgency: {labels: [x], thresholds: {high: 3}}\njira: {done_statuses: [Done]}\n"))
	f.Add([]byte("category_vocabulary: {bug: [fix], feat: []}\ncheck_interpreters: [{type: t, patterns: [p]}]\n"))
	f.Add([]byte("{}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var c config.Config
		if err := yaml.Unmarshal(data, &c); err != nil {
			t.Skip()
		}
		raw, err := json.Marshal(&c)
		if err != nil {
			t.Skip()
		}
		var generic map[string]any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Skip()
		}
		spaced, err := json.MarshalIndent(generic, "\t", "    ")
		if err != nil {
			t.Skip()
		}
		var c2 config.Config
		if err := json.Unmarshal(spaced, &c2); err != nil {
			t.Skip()
		}
		if a, b := ConfigDigest(&c), ConfigDigest(&c2); a != b {
			t.Fatalf("digest differs after a reorder and re-space: %s vs %s\n%s", a, b, spaced)
		}
	})
}

// ---------------------------------------------------------------------------
// The guard: the projection holds every configuration field interpret reads.

// configReads type-checks files as one package and returns the names of the
// fields and methods selected on a value of type *config.Config or
// config.Config, whatever the variable is called.
func configReads(fset *token.FileSet, files []*ast.File) (fields, methods []string, err error) {
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
	if _, err := conf.Check("interpret", fset, files, info); err != nil {
		return nil, nil, err
	}
	fs, ms := map[string]bool{}, map[string]bool{}
	for _, sel := range info.Selections {
		recv := sel.Recv()
		if p, ok := recv.(*types.Pointer); ok {
			recv = p.Elem()
		}
		named, ok := recv.(*types.Named)
		if !ok || named.Obj().Name() != "Config" || named.Obj().Pkg() == nil ||
			!strings.HasSuffix(named.Obj().Pkg().Path(), "/internal/config") {
			continue
		}
		switch sel.Kind() {
		case types.FieldVal:
			fs[sel.Obj().Name()] = true
		case types.MethodVal:
			ms[sel.Obj().Name()] = true
		}
	}
	return sortedNames(fs), sortedNames(ms), nil
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// jsonName is the json tag name of the named field of struct type v.
func jsonName(t *testing.T, v any, field string) string {
	t.Helper()
	sf, ok := reflect.TypeOf(v).FieldByName(field)
	if !ok {
		t.Fatalf("%T has no field %s", v, field)
	}
	return strings.Split(sf.Tag.Get("json"), ",")[0]
}

func projectionTags() map[string]bool {
	tags := map[string]bool{}
	rt := reflect.TypeOf(StampConfig{})
	for i := 0; i < rt.NumField(); i++ {
		tags[strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	return tags
}

// methodTags are the config.Config methods interpret may call, each with the
// projection field that holds its resolved value.
var methodTags = map[string]string{
	"InProgressStatuses": "in_progress_statuses",
	"DoneStatuses":       "done_statuses",
}

// checkProjection reports every field or method read that the projection does
// not hold.
func checkProjection(t *testing.T, fields, methods []string) []string {
	t.Helper()
	tags := projectionTags()
	var missing []string
	for _, f := range fields {
		if !tags[jsonName(t, config.Config{}, f)] {
			missing = append(missing, "field "+f)
		}
	}
	for _, m := range methods {
		if tag, ok := methodTags[m]; !ok || !tags[tag] {
			missing = append(missing, "method "+m)
		}
	}
	return missing
}

func parseDir(t *testing.T, skip ...string) (*token.FileSet, []*ast.File) {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
next:
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		for _, s := range skip {
			if n == s {
				continue next
			}
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return fset, files
}

// INV-REINTERPRET-5: the projection holds every field interpret reads. The
// scan type-checks the package without stamp.go (which reads them all by
// construction), so a field copied into a local or a sub-struct passed under
// another name is still found.
func TestStampConfig_CoversEveryFieldInterpretReads(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the package from source")
	}
	fset, files := parseDir(t, "stamp.go")
	fields, methods, err := configReads(fset, files)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}
	if len(fields) < 8 {
		t.Fatalf("the scan found only %v: it is not seeing the reads", fields)
	}
	if missing := checkProjection(t, fields, methods); len(missing) > 0 {
		t.Fatalf("interpret reads configuration the stamp projection (StampConfig) does not hold: %v\nadd it to StampConfig and NewStampConfig, or the stamp misses a config change", missing)
	}
	// Every field the projection copies is read somewhere (no dead weight that
	// would re-run everything on an unrelated edit).
	read := map[string]bool{}
	for _, f := range fields {
		read[jsonName(t, config.Config{}, f)] = true
	}
	for _, m := range methods {
		read[methodTags[m]] = true
	}
	for tag := range projectionTags() {
		if !read[tag] {
			t.Errorf("StampConfig holds %q but interpret does not read it", tag)
		}
	}
}

// The scan itself finds a read through any variable name and a method not on
// the allowlist, so the guard above cannot pass vacuously.
func TestConfigReads_FindsPlantedReads(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks from source")
	}
	src := `package interpret

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"

func f(whatever *config.Config, byValue config.Config) {
	copied := whatever.TeamMembers
	_ = copied
	_ = byValue.Actor
	_ = whatever.WatchQueries("pr")
	_ = whatever.DoneStatuses()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "planted.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	fields, methods, err := configReads(fset, []*ast.File{f})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(fields), "[Actor TeamMembers]"; got != want {
		t.Fatalf("fields = %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(methods), "[DoneStatuses WatchQueries]"; got != want {
		t.Fatalf("methods = %s, want %s", got, want)
	}
	// Actor is not projected and WatchQueries is not allowlisted: both fail.
	missing := checkProjection(t, fields, methods)
	if got, want := fmt.Sprint(missing), "[field Actor method WatchQueries]"; got != want {
		t.Fatalf("missing = %s, want %s", got, want)
	}
}
