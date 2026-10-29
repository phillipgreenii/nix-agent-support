// Package fakebd is a stand-in for the bd command line, for the fast lane test
// of internal/prchange (TestLaneFake, bead pg2-s4xeju). It keeps the tracker in
// one JSON file, <workspace>/.beads/fake-bd.json, and answers the subset of bd
// the PR lane drives: what pg-connector-issue-beads runs for the lane's issue
// verbs, what pg-pr-backfill runs for a rollback, and what the test's own
// tracker helpers run to seed and change items between runs.
//
// It exists because the real bd starts an embedded dolt engine on every call
// (10 to 60 s each on a loaded host), which made the real-tracker suite run for
// most of an hour. The real-bd suite still runs, by hand, behind the laneE2E
// build tag (TestLaneE2E); this fake is what the gate runs.
//
// Its answers keep bd's shapes where the lane reads them: the {"data": ...}
// envelope of --json (an array for show, update, close, list and comments, an
// object for create and comment), the well-formed error envelope and exit 1 of
// an unknown id, the bd metadata coercion (a --set-metadata value that reads as
// a JSON number or boolean is stored as one), and closed items left out of a
// list that names neither --all nor --status. Anything it does not know (a verb,
// a flag) is an error, never a silent success, so a lane change that starts
// using more of bd fails the fast test loudly instead of passing on a guess.
//
// Comment ids are zero-padded and increase with every comment, so comments made
// within one second still sort in the order they were made (the rollback
// listing breaks a timestamp tie by comment id).
package fakebd

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// StoreFile is the store's name under the workspace's .beads directory.
const StoreFile = "fake-bd.json"

// Issue is one work item, in the field names of `bd list --json`.
type Issue struct {
	ID           string                     `json:"id"`
	Title        string                     `json:"title"`
	Description  string                     `json:"description,omitempty"`
	Status       string                     `json:"status"`
	Priority     int                        `json:"priority"`
	IssueType    string                     `json:"issue_type"`
	Labels       []string                   `json:"labels,omitempty"`
	Assignee     string                     `json:"assignee,omitempty"`
	Parent       string                     `json:"parent,omitempty"`
	CreatedAt    string                     `json:"created_at"`
	CreatedBy    string                     `json:"created_by,omitempty"`
	UpdatedAt    string                     `json:"updated_at"`
	ClosedAt     string                     `json:"closed_at,omitempty"`
	CloseReason  string                     `json:"close_reason,omitempty"`
	Metadata     map[string]json.RawMessage `json:"metadata,omitempty"`
	CommentCount int                        `json:"comment_count"`
}

// Comment is one comment, in the field names of `bd comments --json`.
type Comment struct {
	ID        string `json:"id"`
	IssueID   string `json:"issue_id"`
	Author    string `json:"author,omitempty"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// Store is the whole tracker.
type Store struct {
	Prefix      string            `json:"prefix"`
	NextID      int               `json:"next_id"`
	NextComment int               `json:"next_comment"`
	Config      map[string]string `json:"config,omitempty"`
	Issues      []*Issue          `json:"issues"`
	Comments    []*Comment        `json:"comments"`
}

// Env is what one invocation sees of its process.
type Env struct {
	Args   []string // after the program name
	Dir    string   // the working directory
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

// usageError is a bad invocation (an unknown verb or flag, a missing value).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// notFound is an unknown id; bd answers it with an error envelope as well.
type notFound struct{ id string }

func (e notFound) Error() string { return fmt.Sprintf("no issue found matching %q", e.id) }

// Main runs one invocation and returns its exit code.
func Main(e Env) int {
	if e.Now == nil {
		e.Now = time.Now
	}
	c, err := parseGlobals(e)
	if err == nil {
		err = c.dispatch()
	}
	if err == nil {
		return 0
	}
	var nf notFound
	if errors.As(err, &nf) && c != nil && c.json {
		writeJSON(e.Stdout, map[string]any{"error": nf.Error()})
	}
	fmt.Fprintf(e.Stderr, "Error: %v\n", err)
	return 1
}

// call is one parsed invocation.
type call struct {
	env   Env
	dir   string
	actor string
	verb  string
	args  []string
	json  bool
}

func parseGlobals(e Env) (*call, error) {
	c := &call{env: e, dir: e.Dir}
	args := e.Args
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "-C" || a == "--actor":
			if len(args) < 2 {
				return c, usagef("%s needs a value", a)
			}
			if a == "-C" {
				c.dir = args[1]
			} else {
				c.actor = args[1]
			}
			args = args[2:]
			continue
		case strings.HasPrefix(a, "--actor="):
			c.actor = strings.TrimPrefix(a, "--actor=")
		case a == "--readonly" || a == "-q" || a == "--quiet":
		case a == "--json":
			c.json = true
		case strings.HasPrefix(a, "-"):
			return c, usagef("unknown global flag %q", a)
		default:
			c.verb, c.args = a, args[1:]
			return c, nil
		}
		args = args[1:]
	}
	return c, usagef("no command")
}

// flags is a small flag parser in the shape bd's (cobra) flags take: --name
// value, --name=value, a short alias, and "--" ending the flags.
type flags struct {
	vals  map[string][]string
	bools map[string]bool
	pos   []string
}

// spec names the flags of one verb: name to "value" or "bool"; an alias maps a
// short name onto a long one.
type spec struct {
	kinds   map[string]string
	aliases map[string]string
}

func parseFlags(args []string, s spec) (*flags, error) {
	f := &flags{vals: map[string][]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			f.pos = append(f.pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			f.pos = append(f.pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if long, ok := s.aliases[name]; ok {
			name = long
		}
		kind, ok := s.kinds[name]
		if !ok {
			return nil, usagef("unknown flag %q", a)
		}
		if kind == "bool" {
			if hasVal {
				b, err := strconv.ParseBool(val)
				if err != nil {
					return nil, usagef("flag %q wants a boolean", a)
				}
				f.bools[name] = b
			} else {
				f.bools[name] = true
			}
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return nil, usagef("flag %q needs a value", a)
			}
			i++
			val = args[i]
		}
		f.vals[name] = append(f.vals[name], val)
	}
	return f, nil
}

func (f *flags) last(name string) (string, bool) {
	v := f.vals[name]
	if len(v) == 0 {
		return "", false
	}
	return v[len(v)-1], true
}

func (c *call) dispatch() error {
	switch c.verb {
	case "init":
		return c.init()
	case "config":
		return c.config()
	case "create":
		return c.create()
	case "list":
		return c.list()
	case "show":
		return c.show()
	case "update":
		return c.update()
	case "close":
		return c.close()
	case "reopen":
		return c.reopen()
	case "comment":
		return c.comment()
	case "comments":
		return c.comments()
	case "dep":
		return c.dep()
	case "search":
		return c.search()
	}
	return usagef("unsupported command %q (the fake bd answers only what the PR lane runs)", c.verb)
}

// ---- the store ----------------------------------------------------------------

func (c *call) beadsDir() string { return filepath.Join(c.dir, ".beads") }

// withStore runs f on the store under an exclusive lock, and writes the store
// back when write is set and f succeeded.
func (c *call) withStore(write bool, f func(*Store) error) error {
	lock, err := os.OpenFile(filepath.Join(c.beadsDir(), "fake-bd.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("no fake bd workspace in %s (run init): %w", c.dir, err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	path := filepath.Join(c.beadsDir(), StoreFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no fake bd workspace in %s (run init): %w", c.dir, err)
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("the fake bd store %s is not JSON: %w", path, err)
	}
	if err := f(&s); err != nil {
		return err
	}
	if !write {
		return nil
	}
	return saveStore(path, &s)
}

func saveStore(path string, s *Store) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) find(id string) (*Issue, error) {
	for _, it := range s.Issues {
		if it.ID == id {
			return it, nil
		}
	}
	return nil, notFound{id}
}

func (c *call) stamp() string { return c.env.Now().UTC().Format(time.RFC3339Nano) }

// ---- output --------------------------------------------------------------------

func writeJSON(w io.Writer, data any) {
	b, err := json.Marshal(map[string]any{"data": data, "schema_version": 1})
	if err != nil {
		panic(err) // the shapes here always marshal
	}
	_, _ = w.Write(append(b, '\n'))
}

func (c *call) out(data any, text string) {
	if c.json {
		writeJSON(c.env.Stdout, data)
		return
	}
	fmt.Fprintln(c.env.Stdout, text)
}

// ---- metadata ------------------------------------------------------------------

var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// metaValue is how bd stores a --set-metadata value: a JSON number or boolean
// when it reads as one, a string otherwise.
func metaValue(v string) json.RawMessage {
	if jsonNumber.MatchString(v) || v == "true" || v == "false" {
		return json.RawMessage(v)
	}
	b, _ := json.Marshal(v)
	return b
}

func setMetadata(it *Issue, kvs []string) error {
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return usagef("--set-metadata wants key=value, got %q", kv)
		}
		if it.Metadata == nil {
			it.Metadata = map[string]json.RawMessage{}
		}
		it.Metadata[k] = metaValue(v)
	}
	return nil
}

// ---- the verbs -----------------------------------------------------------------

func (c *call) init() error {
	f, err := parseFlags(c.args, spec{kinds: map[string]string{
		"prefix": "value", "non-interactive": "bool", "quiet": "bool", "skip-agents": "bool", "skip-hooks": "bool",
	}, aliases: map[string]string{"q": "quiet", "p": "prefix"}})
	if err != nil {
		return err
	}
	prefix, ok := f.last("prefix")
	if !ok || prefix == "" {
		return usagef("init needs --prefix")
	}
	if err := os.MkdirAll(c.beadsDir(), 0o700); err != nil {
		return err
	}
	path := filepath.Join(c.beadsDir(), StoreFile)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("a fake bd workspace already exists in %s", c.dir)
	}
	if err := saveStore(path, &Store{Prefix: prefix, NextID: 1, NextComment: 1, Issues: []*Issue{}, Comments: []*Comment{}}); err != nil {
		return err
	}
	if !f.bools["quiet"] {
		fmt.Fprintf(c.env.Stdout, "initialized the fake bd workspace %s (prefix %s)\n", c.dir, prefix)
	}
	return nil
}

func (c *call) config() error {
	if len(c.args) != 3 || c.args[0] != "set" {
		return usagef("the fake bd supports only `config set <key> <value>`")
	}
	return c.withStore(true, func(s *Store) error {
		if s.Config == nil {
			s.Config = map[string]string{}
		}
		s.Config[c.args[1]] = c.args[2]
		return nil
	})
}

var createSpec = spec{kinds: map[string]string{
	"title": "value", "type": "value", "priority": "value", "description": "value", "parent": "value",
	"labels": "value", "metadata": "value", "silent": "bool", "json": "bool",
}, aliases: map[string]string{"t": "type", "p": "priority", "d": "description", "l": "labels"}}

// priorityValue reads a bd priority: 0 to 4, optionally spelled P0 to P4.
func priorityValue(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(v), "P"))
	if err != nil || n < 0 || n > 4 {
		return 0, usagef("invalid priority %q", v)
	}
	return n, nil
}

func (c *call) create() error {
	f, err := parseFlags(c.args, createSpec)
	if err != nil {
		return err
	}
	c.json = c.json || f.bools["json"]
	title, hasTitle := f.last("title")
	switch {
	case hasTitle && len(f.pos) > 0:
		return usagef("create takes the title once (positional or --title)")
	case !hasTitle && len(f.pos) == 1:
		title = f.pos[0]
	case !hasTitle:
		return usagef("create needs a title")
	}
	it := &Issue{Title: title, Status: "open", Priority: 2, IssueType: "task", CreatedBy: c.actor}
	if v, ok := f.last("type"); ok {
		it.IssueType = v
	}
	if v, ok := f.last("priority"); ok {
		if it.Priority, err = priorityValue(v); err != nil {
			return err
		}
	}
	if v, ok := f.last("description"); ok {
		it.Description = v
	}
	if v, ok := f.last("labels"); ok && v != "" {
		r := csv.NewReader(strings.NewReader(v))
		labels, err := r.Read()
		if err != nil {
			return usagef("--labels %q: %v", v, err)
		}
		sort.Strings(labels)
		it.Labels = labels
	}
	if v, ok := f.last("metadata"); ok {
		if err := json.Unmarshal([]byte(v), &it.Metadata); err != nil {
			return usagef("--metadata is not a JSON object: %v", err)
		}
	}
	parent, _ := f.last("parent")
	err = c.withStore(true, func(s *Store) error {
		if parent != "" {
			if _, err := s.find(parent); err != nil {
				return err
			}
			n := 1
			for _, o := range s.Issues {
				if o.Parent == parent {
					n++
				}
			}
			it.ID, it.Parent = fmt.Sprintf("%s.%d", parent, n), parent
		} else {
			it.ID = fmt.Sprintf("%s-%d", s.Prefix, s.NextID)
			s.NextID++
		}
		it.CreatedAt = c.stamp()
		it.UpdatedAt = it.CreatedAt
		s.Issues = append(s.Issues, it)
		return nil
	})
	if err != nil {
		return err
	}
	if f.bools["silent"] {
		fmt.Fprintln(c.env.Stdout, it.ID)
		return nil
	}
	c.out(it, "Created issue: "+it.ID)
	return nil
}

var listSpec = spec{kinds: map[string]string{
	"all": "bool", "limit": "value", "json": "bool", "readonly": "bool", "type": "value", "status": "value",
	"title-contains": "value", "label": "value", "label-any": "value", "parent": "value",
	"metadata-field": "value", "sort": "value",
}, aliases: map[string]string{"n": "limit", "t": "type", "s": "status", "l": "label"}}

func splitCSV(vs []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range vs {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out[p] = true
			}
		}
	}
	return out
}

func hasLabel(it *Issue, l string) bool {
	for _, x := range it.Labels {
		if x == l {
			return true
		}
	}
	return false
}

func metaText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// matches applies a list's filters to one item.
func (f *flags) matches(it *Issue) bool {
	if st := splitCSV(f.vals["status"]); len(st) > 0 {
		if !st[it.Status] {
			return false
		}
	} else if !f.bools["all"] && it.Status == "closed" {
		return false
	}
	if t := splitCSV(f.vals["type"]); len(t) > 0 && !t[it.IssueType] {
		return false
	}
	for _, s := range f.vals["title-contains"] {
		if !strings.Contains(strings.ToLower(it.Title), strings.ToLower(s)) {
			return false
		}
	}
	for _, l := range f.vals["label"] {
		if !hasLabel(it, l) {
			return false
		}
	}
	if anyOf := splitCSV(f.vals["label-any"]); len(anyOf) > 0 {
		found := false
		for l := range anyOf {
			found = found || hasLabel(it, l)
		}
		if !found {
			return false
		}
	}
	if p, ok := f.last("parent"); ok && it.Parent != p {
		return false
	}
	for _, kv := range f.vals["metadata-field"] {
		k, v, _ := strings.Cut(kv, "=")
		raw, ok := it.Metadata[k]
		if !ok || metaText(raw) != v {
			return false
		}
	}
	return true
}

func (c *call) list() error {
	f, err := parseFlags(c.args, listSpec)
	if err != nil {
		return err
	}
	if len(f.pos) > 0 {
		return usagef("list takes no positional argument, got %q", f.pos)
	}
	c.json = c.json || f.bools["json"]
	limit := 50 // bd's own default
	if v, ok := f.last("limit"); ok {
		if limit, err = strconv.Atoi(v); err != nil || limit < 0 {
			return usagef("invalid --limit %q", v)
		}
	}
	if v, ok := f.last("sort"); ok && v != "created" {
		return usagef("the fake bd sorts only by created, got --sort %q", v)
	}
	out := []*Issue{}
	err = c.withStore(false, func(s *Store) error {
		for _, it := range s.Issues {
			if f.matches(it) {
				out = append(out, it)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	ids := make([]string, len(out))
	for i, it := range out {
		ids[i] = it.ID
	}
	c.out(out, strings.Join(ids, "\n"))
	return nil
}

func (c *call) show() error {
	f, err := parseFlags(c.args, spec{kinds: map[string]string{"json": "bool", "readonly": "bool"}})
	if err != nil {
		return err
	}
	c.json = c.json || f.bools["json"]
	if len(f.pos) == 0 {
		return usagef("show needs an id")
	}
	var out []*Issue
	err = c.withStore(false, func(s *Store) error {
		for _, id := range f.pos {
			it, err := s.find(id)
			if err != nil {
				return err
			}
			out = append(out, it)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.out(out, out[0].ID+": "+out[0].Title)
	return nil
}

var updateSpec = spec{kinds: map[string]string{
	"status": "value", "assignee": "value", "title": "value", "description": "value", "priority": "value",
	"add-label": "value", "remove-label": "value", "set-metadata": "value", "unset-metadata": "value",
	"defer": "value", "if-assignee": "value", "if-status": "value", "claim": "bool", "json": "bool",
}, aliases: map[string]string{"s": "status", "a": "assignee", "d": "description", "p": "priority"}}

// setStatus moves an item to status, keeping closed_at in step.
func (c *call) setStatus(it *Issue, status, reason string) {
	it.Status = status
	if status == "closed" {
		it.ClosedAt, it.CloseReason = c.stamp(), reason
	} else {
		it.ClosedAt, it.CloseReason = "", ""
	}
}

func (c *call) applyUpdate(it *Issue, f *flags) error {
	if want, ok := f.last("if-status"); ok && it.Status != want {
		return fmt.Errorf("%s: status is %q, not %q: not updated", it.ID, it.Status, want)
	}
	if want, ok := f.last("if-assignee"); ok && it.Assignee != want {
		return fmt.Errorf("%s: assignee is %q, not %q: not updated", it.ID, it.Assignee, want)
	}
	if v, ok := f.last("defer"); ok && v != "" {
		return usagef("the fake bd only clears a deferral (--defer=), got %q", v)
	}
	if f.bools["claim"] {
		if c.actor == "" {
			return usagef("--claim needs an actor")
		}
		it.Assignee, it.Status = c.actor, "in_progress"
	}
	if v, ok := f.last("status"); ok {
		c.setStatus(it, v, "")
	}
	if v, ok := f.last("assignee"); ok {
		it.Assignee = v
	}
	if v, ok := f.last("title"); ok {
		it.Title = v
	}
	if v, ok := f.last("description"); ok {
		it.Description = v
	}
	if v, ok := f.last("priority"); ok {
		p, err := priorityValue(v)
		if err != nil {
			return err
		}
		it.Priority = p
	}
	for _, l := range f.vals["add-label"] {
		if !hasLabel(it, l) {
			it.Labels = append(it.Labels, l)
		}
	}
	for _, l := range f.vals["remove-label"] {
		var kept []string
		for _, x := range it.Labels {
			if x != l {
				kept = append(kept, x)
			}
		}
		it.Labels = kept
	}
	if len(it.Labels) > 0 {
		sort.Strings(it.Labels)
	}
	if err := setMetadata(it, f.vals["set-metadata"]); err != nil {
		return err
	}
	for _, k := range f.vals["unset-metadata"] {
		delete(it.Metadata, k)
	}
	if len(it.Metadata) == 0 {
		it.Metadata = nil
	}
	it.UpdatedAt = c.stamp()
	return nil
}

// mutate runs change on each named item and prints them.
func (c *call) mutate(ids []string, verbText string, change func(*Issue) error) error {
	if len(ids) == 0 {
		return usagef("%s needs an id", c.verb)
	}
	var out []*Issue
	err := c.withStore(true, func(s *Store) error {
		for _, id := range ids {
			it, err := s.find(id)
			if err != nil {
				return err
			}
			if err := change(it); err != nil {
				return err
			}
			out = append(out, it)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.out(out, verbText+" "+strings.Join(ids, " "))
	return nil
}

func (c *call) update() error {
	f, err := parseFlags(c.args, updateSpec)
	if err != nil {
		return err
	}
	c.json = c.json || f.bools["json"]
	return c.mutate(f.pos, "Updated", func(it *Issue) error { return c.applyUpdate(it, f) })
}

func (c *call) close() error {
	f, err := parseFlags(c.args, spec{
		kinds:   map[string]string{"reason": "value", "force": "bool", "json": "bool"},
		aliases: map[string]string{"r": "reason", "f": "force"},
	})
	if err != nil {
		return err
	}
	c.json = c.json || f.bools["json"]
	reason, _ := f.last("reason")
	return c.mutate(f.pos, "Closed", func(it *Issue) error {
		c.setStatus(it, "closed", reason)
		it.UpdatedAt = c.stamp()
		return nil
	})
}

func (c *call) reopen() error {
	f, err := parseFlags(c.args, spec{
		kinds:   map[string]string{"reason": "value", "json": "bool"},
		aliases: map[string]string{"r": "reason"},
	})
	if err != nil {
		return err
	}
	c.json = c.json || f.bools["json"]
	return c.mutate(f.pos, "Reopened", func(it *Issue) error {
		if it.Status != "closed" {
			return fmt.Errorf("%s is %s, not closed: cannot reopen", it.ID, it.Status)
		}
		c.setStatus(it, "open", "")
		it.UpdatedAt = c.stamp()
		return nil
	})
}

// addComment adds a comment to the item id and returns it.
func (c *call) addComment(id, text string) (*Comment, error) {
	var cm *Comment
	err := c.withStore(true, func(s *Store) error {
		it, err := s.find(id)
		if err != nil {
			return err
		}
		cm = &Comment{ID: fmt.Sprintf("%08d", s.NextComment), IssueID: id, Author: c.actor, Text: text, CreatedAt: c.stamp()}
		s.NextComment++
		s.Comments = append(s.Comments, cm)
		it.CommentCount++
		it.UpdatedAt = cm.CreatedAt
		return nil
	})
	return cm, err
}

func (c *call) comment() error {
	f, err := parseFlags(c.args, spec{kinds: map[string]string{"json": "bool"}})
	if err != nil {
		return err
	}
	c.json = c.json || f.bools["json"]
	if len(f.pos) != 2 {
		return usagef("comment takes an id and a body, got %d arguments", len(f.pos))
	}
	cm, err := c.addComment(f.pos[0], f.pos[1])
	if err != nil {
		return err
	}
	c.out(cm, "Comment added to "+cm.IssueID)
	return nil
}

func (c *call) comments() error {
	f, err := parseFlags(c.args, spec{kinds: map[string]string{"json": "bool"}})
	if err != nil {
		return err
	}
	c.json = c.json || f.bools["json"]
	if len(f.pos) == 3 && f.pos[0] == "add" {
		cm, err := c.addComment(f.pos[1], f.pos[2])
		if err != nil {
			return err
		}
		c.out(cm, "Comment added to "+cm.IssueID)
		return nil
	}
	if len(f.pos) != 1 {
		return usagef("the fake bd supports `comments <id>` and `comments add <id> <text>`")
	}
	out := []*Comment{}
	err = c.withStore(false, func(s *Store) error {
		if _, err := s.find(f.pos[0]); err != nil {
			return err
		}
		for _, cm := range s.Comments {
			if cm.IssueID == f.pos[0] {
				out = append(out, cm)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var texts []string
	for _, cm := range out {
		texts = append(texts, cm.Text)
	}
	c.out(out, strings.Join(texts, "\n"))
	return nil
}

// dep answers `dep list <id>`: the fake tracker has no dependency edges.
func (c *call) dep() error {
	if len(c.args) == 0 || c.args[0] != "list" {
		return usagef("the fake bd supports only `dep list`")
	}
	f, err := parseFlags(c.args[1:], spec{kinds: map[string]string{"direction": "value", "type": "value", "json": "bool"}})
	if err != nil {
		return err
	}
	c.json = c.json || f.bools["json"]
	if len(f.pos) != 1 {
		return usagef("dep list takes one id")
	}
	err = c.withStore(false, func(s *Store) error {
		_, err := s.find(f.pos[0])
		return err
	})
	if err != nil {
		return err
	}
	c.out([]any{}, "no dependencies")
	return nil
}

// search matches the query, case-insensitively, against an item's id and title.
func (c *call) search() error {
	f, err := parseFlags(c.args, spec{
		kinds:   map[string]string{"json": "bool", "readonly": "bool", "limit": "value", "query": "value"},
		aliases: map[string]string{"n": "limit"},
	})
	if err != nil {
		return err
	}
	c.json = c.json || f.bools["json"]
	q, ok := f.last("query")
	if !ok && len(f.pos) == 1 {
		q = f.pos[0]
	}
	q = strings.ToLower(q)
	out := []*Issue{}
	err = c.withStore(false, func(s *Store) error {
		for _, it := range s.Issues {
			if strings.Contains(strings.ToLower(it.ID), q) || strings.Contains(strings.ToLower(it.Title), q) {
				out = append(out, it)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.out(out, fmt.Sprintf("%d results", len(out)))
	return nil
}
