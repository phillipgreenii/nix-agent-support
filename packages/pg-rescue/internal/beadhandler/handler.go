package beadhandler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/orphan"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/tmpldata"
)

// exitFailed is the generic handler failure. A handler MUST NOT let a usage
// error leak exit 2, which the wrapper reads as "declined".
const exitFailed = 1

// rescueLabel names this handler's label on every item it files.
const rescueLabel = "pg-rescue"

// Actions recorded in meta.action.
const (
	ActionCreated   = "created"
	ActionUpdated   = "updated"
	ActionAnnotated = "annotated"
)

// Runtime holds the outside dependencies of Main, injectable for tests. The
// zero value is not usable; start from DefaultRuntime.
type Runtime struct {
	Getenv  func(string) string
	Environ func() []string
	Now     func() time.Time
	// Env feeds the shared template data (random nonce, git toplevel, cwd).
	Env tmpldata.Env
	// Watch starts the orphan watcher; the returned func stops it.
	Watch func(cleanup func()) (stop func())
}

// DefaultRuntime returns a Runtime backed by the real process.
func DefaultRuntime() *Runtime {
	return &Runtime{
		Getenv:  os.Getenv,
		Environ: os.Environ,
		Now:     time.Now,
		Env:     tmpldata.DefaultEnv(),
		Watch:   orphan.Watch,
	}
}

// Main runs the handler and returns its exit code: 3 once an item was
// created, updated or annotated, 1 on any failure (including an unusable or
// unreachable tracker, with no fallback to another one).
func Main(rt *Runtime, args []string, stdout, stderr io.Writer) int {
	o, err := parseOptions(args)
	if errors.Is(err, errHelp) {
		_, _ = io.WriteString(stdout, Usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "pg-rescue-bead: %v\n%s", err, Usage)
		return exitFailed
	}
	if o.printVars {
		return finish(printVars(stdout), stderr)
	}
	if o.printTemplate {
		return finish(printDefaults(stdout), stderr)
	}

	kid := &child{}
	stop := rt.Watch(kid.kill)
	defer stop()

	res, err := run(rt, o, kid)
	if err != nil {
		fmt.Fprintf(stderr, "pg-rescue-bead: %v\n", err)
		return exitFailed
	}
	out, err := res.Render()
	if err != nil {
		fmt.Fprintf(stderr, "pg-rescue-bead: %v\n", err)
		return exitFailed
	}
	if _, err := stdout.Write(out); err != nil {
		fmt.Fprintf(stderr, "pg-rescue-bead: %v\n", err)
		return exitFailed
	}
	return contract.ExitDeferred
}

func finish(err error, stderr io.Writer) int {
	if err != nil {
		fmt.Fprintf(stderr, "pg-rescue-bead: %v\n", err)
		return exitFailed
	}
	return 0
}

// run does the work and returns the deferred result.
func run(rt *Runtime, o *options, kid *child) (contract.Result, error) {
	rep, err := loadReport(rt.Getenv("PG_RESCUE_REPORT"))
	if err != nil {
		return contract.Result{}, err
	}
	data, err := tmpldata.New(rep, rt.Env)
	if err != nil {
		return contract.Result{}, err
	}
	data.OutputTail = lastLines(data.OutputTail, o.tailLines)
	td := templateData{Data: data, FiledAt: rt.Now().UTC().Format(time.RFC3339)}

	top := toplevel(rt.Env, data.Cwd)
	backend, mapped := backendFor(o, top)
	tracker, err := trackerFor(o.trackerDir, top, data.Cwd, mapped)
	if err != nil {
		return contract.Result{}, err
	}
	conn := &connector{env: childEnv(rt.Environ(), tracker, rep.RunID), child: kid, backend: backend}

	body, err := buildBody(o, td)
	if err != nil {
		return contract.Result{}, err
	}

	if o.annotate != "" {
		if err := conn.comment(o.annotate, body); err != nil {
			return contract.Result{}, err
		}
		if len(o.addLabels) > 0 {
			if err := conn.addLabels(o.annotate, o.addLabels); err != nil {
				return contract.Result{}, err
			}
		}
		return result(ActionAnnotated, o.annotate, "Annotated "+o.annotate), nil
	}

	fingerprint := rt.Getenv("PG_RESCUE_FINGERPRINT")
	if fingerprint == "" {
		fingerprint = rep.Fingerprint
	}
	if o.dedupQuery != "" && fingerprint != "" {
		items, err := conn.list(o.dedupQuery)
		if err != nil {
			return contract.Result{}, err
		}
		if match := findOpenMatch(items, fingerprint); match != "" {
			if err := conn.comment(match, body); err != nil {
				return contract.Result{}, err
			}
			return result(ActionUpdated, match, "Updated "+match), nil
		}
	}

	titleText, err := readText(o.titleTemplate, o.hasTitleTemplate, o.titleTemplateFile, DefaultTitleTemplate)
	if err != nil {
		return contract.Result{}, err
	}
	title, err := render("title", titleText, td)
	if err != nil {
		return contract.Result{}, err
	}
	if title = singleLine(title); title == "" {
		return contract.Result{}, errors.New("the title template rendered an empty title")
	}
	spec := createSpec{
		title:       title,
		issueType:   o.issueType,
		priority:    o.priority,
		description: body,
		labels:      labelsFor(o, top),
		metadata:    map[string]string{},
	}
	if fingerprint != "" {
		spec.metadata[metaFingerprint] = fingerprint
	}
	id, err := conn.create(spec)
	if err != nil {
		return contract.Result{}, err
	}
	return result(ActionCreated, id, "Created "+id), nil
}

// result builds the deferred result with its meta.
func result(action, id, summary string) contract.Result {
	meta, _ := json.Marshal(map[string]string{"item_id": id, "action": action})
	return contract.Result{Outcome: contract.Deferred, Summary: summary, Meta: meta}
}

// loadReport reads the report the wrapper wrote. A schema_version this
// handler does not know is refused, as the contract advises.
func loadReport(path string) (*report.Report, error) {
	if path == "" {
		return nil, errors.New("PG_RESCUE_REPORT is not set; this handler runs under pg-rescue")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the report: %w", err)
	}
	var r report.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("cannot parse the report: %w", err)
	}
	if r.SchemaVersion != report.SchemaVersion {
		return nil, fmt.Errorf("unsupported report schema_version %d (this handler reads %d)", r.SchemaVersion, report.SchemaVersion)
	}
	return &r, nil
}

// toplevel is the git toplevel containing cwd, or "" when there is none.
func toplevel(env tmpldata.Env, cwd string) string {
	if env.GitToplevel == nil || cwd == "" {
		return ""
	}
	top, err := env.GitToplevel(cwd)
	if err != nil {
		return ""
	}
	return top
}

// repoKey is what --repo-label-map and --backend-map look a repo up by: the
// basename of the git toplevel containing the failing command's cwd. It is ""
// when there is no toplevel, which neither map ever holds a key for, so neither
// matches a failure outside a git repository.
func repoKey(top string) string {
	if top == "" {
		return ""
	}
	return filepath.Base(top)
}

// backendFor is the pg-connector backend instance every call of this run goes
// through: the --backend-map entry for the repo, else --backend. mapped says
// the entry exists, i.e. the operator named an instance for this repo.
func backendFor(o *options, top string) (backend string, mapped bool) {
	if b, ok := o.backendMap[repoKey(top)]; ok {
		return b, true
	}
	return o.backend, false
}

// trackerFor is the tracker root the handler hands the backend through
// PG_CONNECTOR_ISSUE_BEADS_DIR, or "" when it hands none. That variable only
// serves a backend registered without its own --beads-dir; an instance
// registered with one ignores it. A repo with a --backend-map entry is served
// by an instance the operator named, which is taken to carry its own tracker,
// so the handler looks for none: the repo need not hold a .beads directory,
// and a failure there files through the instance wherever it ran. An explicit
// --tracker-dir is honored either way. Any other repo is served by the generic
// --backend, which has no tracker of its own, so one is required (resolveTracker).
func trackerFor(flagDir, top, cwd string, mapped bool) (string, error) {
	if mapped && flagDir == "" {
		return "", nil
	}
	return resolveTracker(flagDir, top, cwd)
}

// resolveTracker picks the tracker root: --tracker-dir if given, else the git
// toplevel of cwd when it holds a .beads directory. It never guesses.
func resolveTracker(flagDir, top, cwd string) (string, error) {
	if flagDir != "" {
		abs, err := filepath.Abs(flagDir)
		if err != nil {
			return "", fmt.Errorf("--tracker-dir %q: %w", flagDir, err)
		}
		if !isDir(abs) {
			return "", fmt.Errorf("--tracker-dir %q is not a directory", flagDir)
		}
		return abs, nil
	}
	if top != "" && isDir(filepath.Join(top, ".beads")) {
		return top, nil
	}
	return "", fmt.Errorf("no beads tracker found for %s: it is not inside a git repository whose top level has a .beads directory; pass --tracker-dir", cwd)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// childEnv is base with the tracker and the actor set, replacing any value
// base already carried. With no tracker (see trackerFor) the handler selects
// none, and nothing inherited may: both variables a backend would otherwise
// fall back to are removed too, so the tracker is the instance's own or there
// is none, never whichever one the caller's environment happened to name.
func childEnv(base []string, tracker, runID string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, EnvTrackerDir+"=") || strings.HasPrefix(kv, EnvActor+"=") ||
			(tracker == "" && strings.HasPrefix(kv, envBeadsDir+"=")) {
			continue
		}
		out = append(out, kv)
	}
	if tracker != "" {
		out = append(out, EnvTrackerDir+"="+tracker)
	}
	return append(out, EnvActor+"=pg-rescue/"+runID)
}

// labelsFor is pg-rescue, the repo label, then the --label values, without
// repeats. The repo label is --repo-label, else the --repo-label-map entry for
// the git toplevel's basename; with neither there is no repo label, and none
// is guessed.
func labelsFor(o *options, top string) []string {
	repo := o.repoLabel
	if key := repoKey(top); repo == "" && key != "" {
		repo = o.repoLabelMap[key]
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range append([]string{rescueLabel, repo}, o.labels...) {
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// findOpenMatch returns the id of the first item whose recorded fingerprint
// is fingerprint and that is not closed; a closed item never matches.
func findOpenMatch(items []issue, fingerprint string) string {
	for _, it := range items {
		if strings.EqualFold(it.State, "closed") {
			continue
		}
		if it.Metadata[metaFingerprint] == fingerprint {
			return it.ID
		}
	}
	return ""
}

// buildBody renders the body, appends any instructions, and keeps the result
// under the tracker's text cap by shrinking the inlined output tail first.
func buildBody(o *options, td templateData) (string, error) {
	text, err := readText(o.bodyTemplate, o.hasBodyTemplate, o.bodyTemplateFile, DefaultBodyTemplate)
	if err != nil {
		return "", err
	}
	extra, err := readText(o.appendText, o.appendText != "", o.appendFile, "")
	if err != nil {
		return "", err
	}
	var body string
	for lines := lineCount(td.OutputTail); ; lines /= 2 {
		td.OutputTail = lastLines(td.OutputTail, lines)
		body, err = render("body", text, td)
		if err != nil {
			return "", err
		}
		if extra != "" {
			body = strings.TrimRight(body, "\n") + "\n\n" + extra
		}
		if len(body) <= maxBodyBytes || lines == 0 {
			break
		}
	}
	return clampBytes(body, maxBodyBytes), nil
}
