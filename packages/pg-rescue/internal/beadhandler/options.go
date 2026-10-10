// Package beadhandler is pg-rescue-bead: the reference failure handler that
// defers a failure by filing a self-contained bead (or annotating an existing
// one) through pg-connector's beads issue backend. It never calls bd itself
// and adds no policy: its arguments are its entire configuration.
package beadhandler

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

// Defaults for the handler's arguments.
const (
	defaultPriority  = 2
	defaultIssueType = "task"
	defaultTailLines = 200
	// maxPriority is the lowest-urgency bd priority (0 is the highest).
	maxPriority = 4
)

// Usage is the synopsis printed for --help.
const Usage = `usage: pg-rescue-bead [--priority N] [--issue-type T] [--label L]... [--repo-label L]
                      [--repo-label-map REPO=LABEL]... [--tracker-dir PATH]
                      [--backend NAME]
                      [--title-template TEXT | --title-template-file F]
                      [--body-template TEXT | --body-template-file F]
                      [--append-instructions TEXT | --append-instructions-file F]
                      [--output-tail-lines N] [--dedup-query NAME]
                      [--annotate ID [--add-label L]...]
                      [--print-default-template | --print-template-vars]
`

// options are the parsed handler arguments.
type options struct {
	priority     int
	issueType    string
	labels       []string
	repoLabel    string
	repoLabelMap map[string]string
	trackerDir   string
	backend      string

	titleTemplate     string
	hasTitleTemplate  bool
	titleTemplateFile string
	bodyTemplate      string
	hasBodyTemplate   bool
	bodyTemplateFile  string
	appendText        string
	appendFile        string

	tailLines  int
	dedupQuery string
	annotate   string
	addLabels  []string

	printTemplate bool
	printVars     bool
	help          bool
}

// errHelp is returned by parseOptions when --help was requested.
var errHelp = pflag.ErrHelp

// parseOptions parses args. Every error other than errHelp is a usage error,
// which the handler reports as exit 1: exit 2 would be read by the wrapper as
// "declined", so a usage error must never use pflag's customary 2.
func parseOptions(args []string) (*options, error) {
	o := &options{}
	var repoLabelMap []string
	fs := pflag.NewFlagSet("pg-rescue-bead", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&o.priority, "priority", defaultPriority, "bd priority 0-4 (0 is the highest)")
	fs.StringVar(&o.issueType, "issue-type", defaultIssueType, "issue type")
	fs.StringArrayVar(&o.labels, "label", nil, "extra label (repeatable)")
	fs.StringVar(&o.repoLabel, "repo-label", "", "repo label, overriding the --repo-label-map lookup")
	fs.StringArrayVar(&repoLabelMap, "repo-label-map", nil, "REPO=LABEL (repeatable); REPO is the git toplevel's basename")
	fs.StringVar(&o.trackerDir, "tracker-dir", "", "tracker root, overriding the cwd's git toplevel")
	fs.StringVar(&o.backend, "backend", defaultBackend, "pg-connector backend instance name passed as --backend on every call")
	fs.StringVar(&o.titleTemplate, "title-template", "", "title template text")
	fs.StringVar(&o.titleTemplateFile, "title-template-file", "", "title template file")
	fs.StringVar(&o.bodyTemplate, "body-template", "", "body template text")
	fs.StringVar(&o.bodyTemplateFile, "body-template-file", "", "body template file")
	fs.StringVar(&o.appendText, "append-instructions", "", "text appended to the body")
	fs.StringVar(&o.appendFile, "append-instructions-file", "", "file whose text is appended to the body")
	fs.IntVar(&o.tailLines, "output-tail-lines", defaultTailLines, "output tail lines inlined in the body")
	fs.StringVar(&o.dedupQuery, "dedup-query", "", "named pg-connector query listing non-closed pg-rescue items")
	fs.StringVar(&o.annotate, "annotate", "", "comment on this existing item instead of creating one")
	fs.StringArrayVar(&o.addLabels, "add-label", nil, "with --annotate: label to add (repeatable)")
	fs.BoolVar(&o.printTemplate, "print-default-template", false, "print the built-in templates and exit")
	fs.BoolVar(&o.printVars, "print-template-vars", false, "print the template data model and exit")
	fs.BoolVar(&o.help, "help", false, "show usage")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil, errHelp
		}
		return nil, err
	}
	if o.help {
		return nil, errHelp
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	o.hasTitleTemplate = fs.Changed("title-template")
	o.hasBodyTemplate = fs.Changed("body-template")

	if o.printTemplate || o.printVars {
		return o, nil
	}
	if o.priority < 0 || o.priority > maxPriority {
		return nil, fmt.Errorf("--priority must be 0-%d, got %d", maxPriority, o.priority)
	}
	if o.backend == "" {
		return nil, errors.New("--backend must not be empty")
	}
	if o.tailLines < 0 {
		return nil, fmt.Errorf("--output-tail-lines must not be negative, got %d", o.tailLines)
	}
	for _, x := range []struct {
		inline, file string
		inlineSet    bool
		fileSet      bool
	}{
		{"--title-template", "--title-template-file", o.hasTitleTemplate, o.titleTemplateFile != ""},
		{"--body-template", "--body-template-file", o.hasBodyTemplate, o.bodyTemplateFile != ""},
		{"--append-instructions", "--append-instructions-file", fs.Changed("append-instructions"), o.appendFile != ""},
	} {
		if x.inlineSet && x.fileSet {
			return nil, fmt.Errorf("%s and %s are mutually exclusive", x.inline, x.file)
		}
	}
	if o.annotate != "" && o.dedupQuery != "" {
		return nil, errors.New("--annotate and --dedup-query are mutually exclusive: annotate targets one item, dedup searches for one")
	}
	if len(o.addLabels) > 0 && o.annotate == "" {
		return nil, errors.New("--add-label requires --annotate")
	}
	m, err := parseRepoLabelMap(repoLabelMap)
	if err != nil {
		return nil, err
	}
	o.repoLabelMap = m
	return o, nil
}

// parseRepoLabelMap turns REPO=LABEL entries into a map; a later entry for the
// same REPO wins.
func parseRepoLabelMap(entries []string) (map[string]string, error) {
	m := map[string]string{}
	for _, e := range entries {
		repo, label, ok := strings.Cut(e, "=")
		if !ok || repo == "" || label == "" {
			return nil, fmt.Errorf("--repo-label-map %q: want REPO=LABEL with both parts non-empty", e)
		}
		m[repo] = label
	}
	return m, nil
}

// readText returns inline when set, else the contents of file, else def.
func readText(inline string, hasInline bool, file, def string) (string, error) {
	switch {
	case hasInline:
		return inline, nil
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return def, nil
}
