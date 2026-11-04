// Package shapes reduces a decision-DB row to its SHAPE: the tool, the program
// and subcommand, the flag set, the positional path CLASSES and the env names
// the command was run with, and nothing else. Two rows that differ only in a
// literal path within one zone (rm -rf ./build and rm -rf ./dist) collapse to
// one shape, so one label, one golden and one coverage count serve every row of
// the shape (Phase 4, docket tc-o14i5.5).
//
// A shape is derived from the row's tool INPUT only, never from the recorded
// outcome or decision: "the tool ran" is not evidence that the command is safe.
//
// The package reuses internal/cmdparse for the shell grammar and
// internal/cmddesc.DefaultRegistry() for the two questions only a schema can
// answer: does a program have subcommands, and does a flag take a value.
package shapes

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// Unparsed is the program and family of a Bash input that could not be parsed,
// or that holds no simple command at all.
const Unparsed = "unparsed"

// ToolBash is the tool whose input is shell text.
const ToolBash = "Bash"

const (
	listSep = "\x1f" // separates elements inside a joined Shape field
	pairSep = "\x1e" // separates program from subcommand inside a Seq element
)

// Shape is the comparable reduction of one row. Every list-valued field is
// sorted, de-duplicated and joined with listSep so the struct can be a map key.
// Seq keeps the ORDER of the simple commands (it is not sorted): it holds one
// program+subcommand pair per simple command, wrappers included.
type Shape struct {
	Tool        string
	Program     string // first non-wrapper simple command; Unparsed for an unparseable/empty input
	Subcommand  string
	Flags       string
	PathClasses string
	EnvNames    string
	Seq         string
}

// Wrappers are the programs that run another command (or, for cd, only set the
// scene for it); the program, subcommand and family of a compound command are
// those of the first simple command that is not one of them.
var wrappers = map[string]bool{
	"cd": true, "env": true, "sudo": true, "time": true, "nice": true, "nohup": true, "command": true,
}

// IsWrapper reports whether program is one of the wrapper programs.
func IsWrapper(program string) bool { return wrappers[program] }

// FlagList, PathClassList and EnvNameList return the list fields split back
// into slices (never nil, so they encode as JSON arrays).
func (s Shape) FlagList() []string      { return split(s.Flags) }
func (s Shape) PathClassList() []string { return split(s.PathClasses) }
func (s Shape) EnvNameList() []string   { return split(s.EnvNames) }

// Programs lists the program of EVERY simple command, in order, wrappers
// included; it is empty for a non-Bash shape and for an unparsed one.
func (s Shape) Programs() []string {
	out := []string{}
	for _, p := range split(s.Seq) {
		prog, _, _ := strings.Cut(p, pairSep)
		out = append(out, prog)
	}
	return out
}

// Family is the first program that is not a wrapper, the tool name for a
// non-Bash shape, and Unparsed for an unparsed Bash input. A command made only
// of wrappers (a bare cd) is its own family.
func (s Shape) Family() string {
	if s.Tool != ToolBash {
		return s.Tool
	}
	return s.Program
}

// Key is the stable canonical string written as shape_key. Every element is
// escaped so none of the separators below can occur inside one, and the key is
// a single printable line. It is opaque to every consumer.
func (s Shape) Key() string {
	var b strings.Builder
	b.WriteString("tool=" + esc(s.Tool))
	b.WriteString("|prog=" + esc(s.Program))
	b.WriteString("|sub=" + esc(s.Subcommand))
	b.WriteString("|seq=")
	for i, p := range split(s.Seq) {
		if i > 0 {
			b.WriteString(">")
		}
		prog, sub, _ := strings.Cut(p, pairSep)
		b.WriteString(esc(prog) + "/" + esc(sub))
	}
	b.WriteString("|flags=" + joinEsc(s.Flags))
	b.WriteString("|paths=" + joinEsc(s.PathClasses))
	b.WriteString("|env=" + joinEsc(s.EnvNames))
	return b.String()
}

func joinEsc(joined string) string {
	parts := split(joined)
	for i := range parts {
		parts[i] = esc(parts[i])
	}
	return strings.Join(parts, ",")
}

func split(joined string) []string {
	if joined == "" {
		return []string{}
	}
	return strings.Split(joined, listSep)
}

// esc percent-escapes the characters Key uses as separators, plus control
// characters, so distinct elements can never render to the same key text.
func esc(s string) string {
	if !strings.ContainsAny(s, "%|,>/") && !hasControl(s) {
		return s
	}
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 && (r < 0x20 || r == 0x7f || strings.ContainsRune("%|,>/", r)) {
			b.WriteByte('%')
			b.WriteByte(hex[r>>4])
			b.WriteByte(hex[r&0xf])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

var (
	registryOnce sync.Once
	registry     cmddesc.Registry
)

func defaultRegistry() cmddesc.Registry {
	registryOnce.Do(func() { registry = cmddesc.DefaultRegistry() })
	return registry
}

// Normalize reduces a row to its shape. It never reads the recorded decision or
// outcome.
func Normalize(row asklog.DecisionRow) Shape {
	if row.ToolName != ToolBash {
		return normalizeNonBash(row)
	}
	return normalizeBash(row)
}

// ---- non-Bash tools ----

func normalizeNonBash(row asklog.DecisionRow) Shape {
	s := Shape{Tool: row.ToolName}
	var in map[string]json.RawMessage
	_ = json.Unmarshal([]byte(row.ToolInputJSON), &in)
	str := func(key string) string {
		var v string
		if raw, ok := in[key]; ok {
			_ = json.Unmarshal(raw, &v)
		}
		return v
	}
	switch {
	case row.ToolName == "Skill":
		s.Subcommand = str("skill")
	case row.ToolName == "Agent":
		s.Subcommand = str("subagent_type")
	case strings.HasPrefix(row.ToolName, "mcp__"):
		s.Subcommand = row.ToolName
	}
	switch row.ToolName {
	case "Read", "Write", "Edit", "MultiEdit":
		if p := str("file_path"); p != "" {
			s.PathClasses = PathClass(p, row.CWD)
		}
	case "Glob", "Grep":
		if p := str("path"); p != "" {
			s.PathClasses = PathClass(p, row.CWD)
		}
	}
	return s
}

// ---- Bash ----

// stage is one simple command after wrapper handling.
type stage struct {
	program string
	args    []string
}

type bashAcc struct {
	cwd     string
	pairs   []string
	flags   map[string]bool
	classes map[string]bool
	env     map[string]bool
	// first non-wrapper stage's program and subcommand
	prog, sub string
	haveFirst bool
	// fallback when every stage is a wrapper
	firstProg, firstSub string
	haveAny             bool
}

func normalizeBash(row asklog.DecisionRow) Shape {
	unparsed := Shape{Tool: ToolBash, Program: Unparsed}
	var in struct {
		Command *string `json:"command"`
	}
	if err := json.Unmarshal([]byte(row.ToolInputJSON), &in); err != nil || in.Command == nil || strings.TrimSpace(*in.Command) == "" {
		return unparsed
	}
	parse := cmdparse.ParseShell(*in.Command)
	if parse.Unparseable {
		return unparsed
	}
	acc := &bashAcc{cwd: row.CWD, flags: map[string]bool{}, classes: map[string]bool{}, env: map[string]bool{}}
	for _, leaf := range parse.Leaves {
		acc.addLeaf(leaf)
	}
	if len(acc.pairs) == 0 {
		// Nothing ran (a bare assignment, a loop word list): no program.
		s := unparsed
		s.EnvNames = joinSorted(acc.env)
		return s
	}
	s := Shape{Tool: ToolBash, Seq: strings.Join(acc.pairs, listSep)}
	if acc.haveFirst {
		s.Program, s.Subcommand = acc.prog, acc.sub
	} else {
		s.Program, s.Subcommand = acc.firstProg, acc.firstSub
	}
	s.Flags = joinSorted(acc.flags)
	s.PathClasses = joinSorted(acc.classes)
	s.EnvNames = joinSorted(acc.env)
	return s
}

func (a *bashAcc) addLeaf(leaf cmdparse.ParsedCommand) {
	for _, ev := range leaf.EnvVars {
		a.env[ev.Name] = true
	}
	for _, r := range leaf.Redirections {
		if r.Path != "" {
			a.classes[PathClass(r.Path, a.cwd)] = true
		}
	}
	if leaf.Executable == "" {
		return
	}
	exe := path.Base(leaf.Executable)
	// The parser unwraps env/nohup/time/nice; Raw still shows them.
	for _, w := range leadingWrappers(leaf.Raw, exe) {
		a.addStage(stage{program: w}, true)
	}
	st := stage{program: exe, args: leaf.Args}
	for wrappers[st.program] && st.program != "cd" {
		inner, ok := a.unwrap(st)
		a.addStage(stage{program: st.program}, true)
		if !ok {
			return
		}
		st = inner
	}
	a.addStage(st, false)
}

// addStage records one simple command. A wrapper stage contributes only its
// program to the sequence; a real command also contributes subcommand, flags
// and operand classes.
func (a *bashAcc) addStage(st stage, wrapperOnly bool) {
	sub := ""
	if !wrapperOnly {
		sub = a.scan(st)
	} else if st.program != "" && len(st.args) > 0 {
		a.scan(st)
	}
	a.pairs = append(a.pairs, st.program+pairSep+sub)
	if !a.haveAny {
		a.firstProg, a.firstSub, a.haveAny = st.program, sub, true
	}
	if !wrappers[st.program] && !a.haveFirst {
		a.prog, a.sub, a.haveFirst = st.program, sub, true
	}
}

// leadingWrappers returns the wrapper programs written before exe in raw, which
// the parser dropped when it unwrapped them. It returns nil when exe cannot be
// found among raw's words.
func leadingWrappers(raw, exe string) []string {
	var out []string
	for _, w := range strings.Fields(raw) {
		if isAssignment(w) {
			continue
		}
		b := path.Base(w)
		if b == exe {
			return out
		}
		if wrappers[b] {
			out = append(out, b)
		}
	}
	return nil
}

func isAssignment(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for j, r := range w[:i] {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (j > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// unwrap peels one wrapper off st. Wrapper flags are recorded by name only and
// wrapper VAR=value tokens as env names. ok is false when no inner command
// follows, or the wrapper is being used as a query (command -v).
func (a *bashAcc) unwrap(st stage) (stage, bool) {
	valueFlags := wrapperValueFlags[st.program]
	args := st.args
	i := 0
	for i < len(args) {
		t := args[i]
		switch {
		case t == "--":
			i++
			goto inner
		case isAssignment(t) && (st.program == "env" || st.program == "sudo"):
			a.env[t[:strings.IndexByte(t, '=')]] = true
			i++
		case strings.HasPrefix(t, "-") && len(t) > 1:
			name, _, attached := strings.Cut(t, "=")
			if st.program == "command" && (name == "-v" || name == "-V") {
				return stage{}, false
			}
			a.flags[name] = true
			i++
			if !attached && valueFlags[name] && i < len(args) {
				i++
			}
		default:
			goto inner
		}
	}
inner:
	if i >= len(args) {
		return stage{}, false
	}
	return stage{program: path.Base(args[i]), args: args[i+1:]}, true
}

var wrapperValueFlags = map[string]map[string]bool{
	"sudo": set("-u", "-g", "-h", "-p", "-C", "-D", "-R", "-T", "-U", "-r", "-t",
		"--user", "--group", "--host", "--prompt", "--close-from", "--chdir", "--chroot",
		"--command-timeout", "--role", "--type", "--other-user"),
	"env":  set("-u", "-C", "-S", "--unset", "--chdir", "--split-string"),
	"time": set("-f", "-o", "--format", "--output"),
	"nice": set("-n", "--adjustment"),
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// ---- argument scanning ----

// pinnedValueFlags is the danger-shaped flag list of the linter plus the
// target-selecting flags. A flag matching one contributes name=value, so
// `kubectl --context prod delete` and `kubectl --context dev delete`, or
// `curl -X DELETE` and `curl -X GET`, are different shapes. `*` matches any
// characters, hyphens included.
var pinnedValueFlags = []string{
	"-o", "--output*", "--exec*", "-c", "--config*", "--command", "-e",
	"--*-program", "--*-hook*", "--receive-pack", "--upload-pack", "--prune",
	"--mirror", "--all", "--delete", "-f", "--force*", "-r", "-R", "--recursive",
	"--context", "--server", "--kubeconfig", "--host", "-H", "--header", "--url",
	"-X", "--request", "-n", "--namespace", "--cluster", "--address",
}

func isPinned(name string) bool {
	for _, p := range pinnedValueFlags {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

const maxValueLen = 64

// flagValue renders the literal value of a pinned flag.
func flagValue(v string) string {
	if strings.ContainsAny(v, "$`*?[") {
		return "<expansion>"
	}
	if r := []rune(v); len(r) > maxValueLen {
		return string(r[:maxValueLen])
	}
	return v
}

// scan reads one command's arguments: it records flags and operand path
// classes into the accumulator and returns the subcommand. A flag takes a value
// only when the program's schema says so; for a pinned-list flag the schema
// cannot rule out (undeclared, or no schema at all) the next token that does
// not start with "-" is its value.
func (a *bashAcc) scan(st stage) string {
	schema, hasSchema := defaultRegistry().Lookup(st.program)
	hasSub := hasSchema && (len(schema.Subcommands) > 0 || schema.DefaultSubcommand != "" || schema.VerbFamily != "")
	sub, haveSub := "", false

	// flagInfo: does the schema declare name, and how many values does it take?
	flagInfo := func(name string) (declared bool, takes int) {
		if !hasSchema {
			return false, 0
		}
		fs, ok := schema.Flags[name]
		if !ok {
			return false, 0
		}
		switch fs.Arity {
		case cmddesc.ArityOne:
			return true, 1
		case cmddesc.ArityN:
			n := len(fs.Operands)
			if n < 1 {
				n = 1
			}
			return true, n
		}
		return true, 0
	}

	args := st.args
	endOpts := false
	for i := 0; i < len(args); i++ {
		t := args[i]
		if !endOpts && t == "--" {
			endOpts = true
			continue
		}
		if endOpts || !isFlagToken(t) {
			if hasSub && !haveSub && !endOpts {
				if _, isKey := schema.Subcommands[t]; isKey || schema.DefaultSubcommand == "" {
					sub, haveSub = t, true
					continue
				}
			}
			a.classes[PathClass(t, a.cwd)] = true
			continue
		}

		// consumeNext takes the next token as this flag's value.
		next := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		// emit records a flag, with its value when it is pinned.
		emit := func(name, value string, haveValue bool) {
			if haveValue && isPinned(name) {
				a.flags[name+"="+flagValue(value)] = true
			} else {
				a.flags[name] = true
			}
		}
		// resolve handles one flag whose name is known and whose value, if it
		// was glued on, is attached.
		resolve := func(name, attached string, hasAttached bool) {
			declared, takes := flagInfo(name)
			switch {
			case hasAttached:
				emit(name, attached, true)
				// ArityN still wants its remaining tokens.
				for k := 1; k < takes; k++ {
					next()
				}
			case takes > 0:
				v, ok := next()
				for k := 1; k < takes; k++ {
					next()
				}
				emit(name, v, ok)
			case !declared && isPinned(name):
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					v, _ := next()
					emit(name, v, true)
				} else {
					emit(name, "", false)
				}
			default:
				emit(name, "", false)
			}
		}

		if strings.HasPrefix(t, "--") {
			name, val, has := strings.Cut(t, "=")
			resolve(name, val, has)
			continue
		}
		// A single-dash token that the schema declares whole (find's -name,
		// go's -run) is one flag; otherwise it is a cluster of short flags.
		if name, val, has := strings.Cut(t, "="); has {
			if declared, _ := flagInfo(name); declared || len(name) == 2 {
				resolve(name, val, true)
				continue
			}
		}
		if declared, _ := flagInfo(t); declared && len(t) > 2 {
			resolve(t, "", false)
			continue
		}
		cluster := t[1:]
		for j := 0; j < len(cluster); j++ {
			name := "-" + string(cluster[j])
			rest := cluster[j+1:]
			declared, takes := flagInfo(name)
			switch {
			case takes > 0 && rest != "":
				emit(name, rest, true) // -n3
				j = len(cluster)
			case takes > 0:
				resolve(name, "", false)
			case rest == "" && !declared && isPinned(name):
				resolve(name, "", false)
			default:
				emit(name, "", false)
			}
		}
	}
	return sub
}

// ---- path classes ----

// PathClass assigns a positional operand (or a file tool's path input) exactly
// one class: the FIRST that matches in this ordered list. cwd is the row's
// working directory; the process HOME is never consulted, so shape keys do not
// depend on the machine.
func PathClass(p, cwd string) string {
	base := path.Base(strings.TrimRight(p, "/"))
	segs := strings.Split(p, "/")
	hasSeg := func(s string) bool {
		for _, g := range segs {
			if g == s {
				return true
			}
		}
		return false
	}
	under := func(dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }
	abs := strings.HasPrefix(p, "/")
	switch {
	case strings.Contains(p, "://"):
		return "url"
	case isSecretFormat(base) || hasSeg(".ssh"):
		return "secret-format"
	case hasSeg(".claude") || base == ".mcp.json" || base == "CLAUDE.md" || base == "CLAUDE.local.md":
		return "agent-control"
	case hasSeg(".git"):
		return "git-internal"
	case strings.ContainsAny(p, "$`*?[{"):
		return "expansion"
	case strings.HasPrefix(p, "~") || strings.HasPrefix(p, "/home/") || strings.HasPrefix(p, "/Users/"):
		return "home"
	case under("/tmp") || under("/var/tmp"):
		return "temp"
	case abs && isSystem(p):
		return "system"
	case p == ".." || strings.HasPrefix(p, "../"):
		return "parent-escape"
	case strings.HasPrefix(p, ":"):
		return "ref-delete"
	case strings.HasPrefix(p, "-"):
		return "flag-like"
	case abs && !(cwd != "" && under(strings.TrimRight(cwd, "/"))):
		return "other-absolute"
	case strings.Contains(p, "/") || strings.HasPrefix(p, "."):
		return "project"
	}
	return "word"
}

func isSecretFormat(base string) bool {
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".pem") || strings.HasPrefix(base, "id_") {
		return true
	}
	switch base {
	case ".netrc", ".pgpass", ".git-credentials":
		return true
	}
	return false
}

func isSystem(p string) bool {
	for _, d := range []string{"/etc", "/usr", "/bin", "/sbin", "/var", "/opt", "/dev", "/proc", "/sys", "/nix", "/run"} {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

func joinSorted(m map[string]bool) string {
	if len(m) == 0 {
		return ""
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, listSep)
}

// isFlagToken reports whether t is spelled like an option: "-" or "--" followed
// by a letter or digit. Anything else that starts with "-" ("-", "--", "---",
// "-=", an echoed banner) is an operand of class flag-like.
func isFlagToken(t string) bool {
	alnum := func(b byte) bool {
		return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
	}
	switch {
	case len(t) >= 3 && t[0] == '-' && t[1] == '-':
		return alnum(t[2])
	case len(t) >= 2 && t[0] == '-':
		return t[1] != '-' && alnum(t[1])
	}
	return false
}
