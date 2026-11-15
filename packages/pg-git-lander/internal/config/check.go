package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/glob"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// CheckOptions are the inputs of config check (design section 12.5).
type CheckOptions struct {
	// RepoDir is the directory whose repository the repo file belongs to.
	RepoDir string
	// File, when set, is an (uncommitted) repo file to validate instead of
	// the one on the primary branch's tip.
	File string
}

// Check validates the XDG file and the repo file as the next operation
// would read them and returns the config-ok outcome with its warnings, a
// config-unrunnable outcome (same warnings) when a configured command is
// not on this process's PATH or is a missing absolute path, or a
// config-error outcome naming the file, line, key and layer. The returned
// error is for failures that are not about the configuration (RepoDir is
// not a repository, say).
func (l *Loader) Check(ctx context.Context, opts CheckOptions) (*outcome.Outcome, error) {
	pre, err := l.checkXDG(ctx, opts.RepoDir)
	if err != nil {
		var ce *Error
		if errors.As(err, &ce) {
			return configErrorOutcome(ce, ""), nil
		}
		return nil, err
	}
	var warns []Warning
	if pre.xdgFound {
		src, _ := os.ReadFile(pre.xdgPath)
		if doc, _ := parseXDG(src); doc != nil {
			for _, v := range append([]*vals{doc.defaults}, doc.repos...) {
				warns = append(warns, l.commandWarnings(ctx, v, pre.xdgPath, pre)...)
			}
		}
	}

	var repoVals *vals
	switch {
	case opts.File != "":
		src, err := os.ReadFile(opts.File)
		if err != nil {
			return configErrorOutcome(&Error{
				Word: outcome.ConfigError, Message: err.Error(),
				Loc: &outcome.ConfigErr{File: opts.File, Layer: LayerRepoFile},
			}, "repo-config-error"), nil
		}
		repoVals, err = l.checkRepoBytes(src, opts.File, &warns)
		if err != nil {
			return configErrorOutcome(err.(*Error), "repo-config-error"), nil
		}
	case !pre.IgnoreRepoConfig:
		ref := pre.RebaseTip(ctx)
		src, display, err := pre.repoFileAt(ctx, ref)
		if err != nil {
			return nil, err
		}
		if src != nil {
			repoVals, err = l.checkRepoBytes(src, display, &warns)
			if err != nil {
				return configErrorOutcome(err.(*Error), "repo-config-error"), nil
			}
		}
	}
	if repoVals != nil {
		name := opts.File
		if name == "" {
			name = pre.RebaseTip(ctx) + ":" + RepoFileName
		}
		warns = append(warns, l.commandWarnings(ctx, repoVals, name, pre)...)
	}
	warns = append(warns, l.lossyDriverWarnings(ctx, pre)...)

	o := outcome.New(outcome.ConfigOK)
	var unrunnable []string
	for _, w := range warns {
		if w.unrunnable {
			unrunnable = append(unrunnable, fmt.Sprintf("%s (key %s, %s:%d) %s", w.cmd, w.Key, w.File, w.Line, w.problem))
		}
	}
	if len(unrunnable) > 0 {
		o = outcome.New(outcome.ConfigUnrunnable)
		o.Reason = string(outcome.WarnCommandNotFound)
		o.Message = strings.Join(unrunnable, "; ")
		o.Hint = "PATH here means this process's PATH: run config check under the PATH the land will use."
	}
	for _, w := range warns {
		o.Warnings = append(o.Warnings, outcome.WarningEntry{
			Warning: w.Word, Reason: w.Key,
			Detail: fmt.Sprintf("%s:%d: %s", w.File, w.Line, w.detail),
		})
	}
	return o, nil
}

func configErrorOutcome(e *Error, reason string) *outcome.Outcome {
	o := outcome.New(outcome.ConfigError)
	o.Reason = reason
	o.Message = e.Message
	o.Hint = e.Hint
	if e.Loc != nil {
		loc := *e.Loc
		o.ConfigError = &loc
		o.Path = loc.File
		o.Detail = e.Error()
	} else {
		o.Detail = e.Message
	}
	return o
}

// checkXDG validates the XDG file (every [[repo]] root must be inside a git
// clone) and returns the first stage of loading.
func (l *Loader) checkXDG(ctx context.Context, repoDir string) (*Pre, error) {
	path := l.XDGPath()
	if src, err := os.ReadFile(path); err == nil {
		doc, errs := parseXDG(src)
		if len(errs) == 0 {
			for i, e := range doc.repos {
				if _, err := l.Git.CommonDir(ctx, e.root); err != nil {
					errs = append(errs, issue{
						key: fmt.Sprintf("repo[%d].root", i), line: e.rootLine,
						msg: fmt.Sprintf("root %q is not inside a git clone", e.root),
					})
				}
			}
		}
		if len(errs) > 0 {
			sort.SliceStable(errs, func(i, j int) bool { return errs[i].line < errs[j].line })
			e := xdgError(path, errs[0])
			if len(errs) > 1 {
				e.Message += fmt.Sprintf(" (and %d more problem(s) in the file)", len(errs)-1)
			}
			return nil, e
		}
	}
	return l.LoadXDG(ctx, repoDir)
}

func (l *Loader) checkRepoBytes(src []byte, display string, warns *[]Warning) (*vals, error) {
	v, errs, unknown := parseRepoFile(src)
	if len(errs) > 0 {
		return nil, &Error{
			Word: outcome.ConfigError, Reason: outcome.RefRepoConfigError, Message: errs[0].msg,
			Loc:  &outcome.ConfigErr{File: display, Line: errs[0].line, Key: errs[0].key, Layer: LayerRepoFile},
			Hint: outcome.Refusals[outcome.RefRepoConfigError].Hint,
		}
	}
	for _, u := range unknown {
		*warns = append(*warns, Warning{
			Word: outcome.WarnUnknownRepoConfigKey, File: display, Line: u.line, Key: u.key,
			detail: "unknown key (ignored)",
		})
	}
	return v, nil
}

// repoFileAt returns the repo file's bytes at ref's tip (nil when absent).
func (p *Pre) repoFileAt(ctx context.Context, ref string) ([]byte, string, error) {
	g := p.l.Git
	display := ref + ":" + RepoFileName
	r := g.Run(ctx, p.Repo, "ls-tree", "--full-tree", ref, "--", RepoFileName)
	if !r.OK() {
		return nil, display, nil // no such ref: nothing to read
	}
	line := strings.TrimSpace(string(r.Stdout))
	if line == "" {
		return nil, display, nil
	}
	meta, _, _ := strings.Cut(line, "\t")
	f := strings.Fields(meta)
	if len(f) != 3 || f[1] != "blob" || f[0] == "120000" {
		return nil, display, &Error{
			Word: outcome.ConfigError, Reason: outcome.RefRepoConfigError, Message: "must be a regular file",
			Loc: &outcome.ConfigErr{File: display, Layer: LayerRepoFile},
		}
	}
	b := g.Run(ctx, p.Repo, "cat-file", "blob", f[2])
	if !b.OK() {
		return nil, display, fmt.Errorf("config: reading blob %s: %s", f[2], strings.TrimSpace(string(b.Stderr)))
	}
	if b.Stdout == nil {
		b.Stdout = []byte{}
	}
	return b.Stdout, display, nil
}

// commandWarnings warns about relative command paths and commands that do
// not exist, for every command a layer names. A relative command of a
// run_per = dir conflict rule runs inside each matched directory, so it is
// looked up in each directory matched at the primary branch tip (check has no
// branch checkout); every other relative command is looked up in the repo root.
func (l *Loader) commandWarnings(ctx context.Context, v *vals, file string, pre *Pre) []Warning {
	repo := pre.Repo
	var out []Warning
	check := func(argv []string, line int, key string, rule *Rule) {
		if len(argv) == 0 {
			return
		}
		cmd := argv[0]
		switch {
		case filepath.IsAbs(cmd):
			if !isFile(cmd) {
				out = append(out, Warning{
					Word: outcome.WarnCommandNotFound, File: file, Line: line, Key: key, detail: cmd + " does not exist",
					unrunnable: true, cmd: cmd, problem: "does not exist",
				})
			}
		case strings.Contains(cmd, "/"):
			out = append(out, Warning{
				Word: outcome.WarnRelativeCommandPath, File: file, Line: line, Key: key,
				detail: cmd + " runs a file from the work worktree, which holds the branch's content; acceptable only for a trusted repo",
			})
			if rule != nil && rule.RunPer == "dir" && rule.Glob != "" {
				if w := l.dirCommandWarning(ctx, pre, rule, cmd, file, line, key); w != nil {
					out = append(out, *w)
				}
			} else if !isFile(filepath.Join(repo, cmd)) {
				out = append(out, Warning{Word: outcome.WarnCommandNotFound, File: file, Line: line, Key: key, detail: cmd + " does not exist in the repo"})
			}
		default:
			if _, err := exec.LookPath(cmd); err != nil {
				out = append(out, Warning{
					Word: outcome.WarnCommandNotFound, File: file, Line: line, Key: key, detail: cmd + " is not on PATH",
					unrunnable: true, cmd: cmd, problem: "is not on PATH",
				})
			}
		}
	}
	for _, op := range operations {
		for _, pt := range points {
			for _, h := range v.hooks[op][pt] {
				check(h.Run, h.Line, h.Key+".run", nil)
			}
		}
	}
	rules := append([]Rule{}, v.rules...)
	if v.conflictDefault != nil {
		rules = append(rules, *v.conflictDefault)
	}
	for i := range rules {
		r := &rules[i]
		check(r.Resolve, r.Line, r.Key+".resolve", r)
		check(r.Verify, r.Line, r.Key+".verify", r)
	}
	return out
}

// maxListedDirs is how many directories a dir-rule warning names.
const maxListedDirs = 5

// dirCommandWarning tests a relative cmd of a run_per = dir rule against each
// directory the rule's glob matches at the primary branch tip (path.Dir of
// each matched file, as the resolver's units do). It returns one warning
// naming the first maxListedDirs directories that lack cmd, or nil.
func (l *Loader) dirCommandWarning(ctx context.Context, pre *Pre, r *Rule, cmd, file string, line int, key string) *Warning {
	g := l.Git
	tip := pre.RebaseTip(ctx)
	ls := g.Run(ctx, pre.Repo, "ls-tree", "-r", "--name-only", "-z", tip)
	if !ls.OK() {
		return nil
	}
	seen := map[string]bool{}
	var dirs []string
	for _, p := range strings.Split(string(ls.Stdout), "\x00") {
		if p == "" || !glob.Match(r.Glob, p) {
			continue
		}
		if d := path.Dir(p); !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	var missing []string
	for _, d := range dirs {
		if !g.Run(ctx, pre.Repo, "cat-file", "-e", tip+":"+path.Join(d, cmd)).OK() {
			missing = append(missing, d)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	list := strings.Join(missing[:min(len(missing), maxListedDirs)], ", ")
	if len(missing) > maxListedDirs {
		list += fmt.Sprintf(" and %d more", len(missing)-maxListedDirs)
	}
	return &Warning{
		Word: outcome.WarnCommandNotFound, File: file, Line: line, Key: key,
		detail: fmt.Sprintf("%s does not exist in %d of %d matched director(ies) at the primary branch tip (%s): %s; this inspects the primary branch tip, not the branch's own content", cmd, len(missing), len(dirs), tip, list),
	}
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

var (
	mergeDriverRe = regexp.MustCompile(`^merge\.(.+)\.driver$`)
	mergeAttrRe   = regexp.MustCompile(`(?:^|\s)merge=(\S+)`)
	lossyNameRe   = regexp.MustCompile(`(?i)keep[-_]?ours|(^|[-_.])ours($|[-_.])`)
)

// lossyCommand reports a driver command that keeps one side whole: true,
// a bare colon, or one that names a keep-ours script.
func lossyCommand(cmd string) bool {
	c := strings.TrimSpace(cmd)
	return c == "true" || c == ":" || lossyNameRe.MatchString(c)
}

// lossyDriverWarnings warns about merge drivers that keep one side whole,
// found in git config (merge.<name>.driver) or named by a merge= attribute.
// During a rebase "ours" is the upstream, so the branch's own change to such
// a file would be dropped without a conflict stop (design section 16).
func (l *Loader) lossyDriverWarnings(ctx context.Context, pre *Pre) []Warning {
	g := l.Git
	cmds := map[string]string{}
	if r := g.Run(ctx, pre.Repo, "config", "--get-regexp", `^merge\..*\.driver$`); r.OK() {
		for _, ln := range strings.Split(string(r.Stdout), "\n") {
			k, val, _ := strings.Cut(strings.TrimSpace(ln), " ")
			if m := mergeDriverRe.FindStringSubmatch(k); m != nil {
				cmds[m[1]] = val
			}
		}
	}
	attrSrc := map[string]string{} // driver name -> where merge= names it
	addAttrs := func(text, where string) {
		for _, ln := range strings.Split(text, "\n") {
			if m := mergeAttrRe.FindStringSubmatch(ln); m != nil {
				if _, ok := attrSrc[m[1]]; !ok {
					attrSrc[m[1]] = where
				}
			}
		}
	}
	tip := pre.RebaseTip(ctx)
	if r := g.Run(ctx, pre.Repo, "grep", "-I", "-h", "-e", "merge=", tip, "--", ".gitattributes", "*.gitattributes"); r.OK() {
		addAttrs(string(r.Stdout), tip+":.gitattributes")
	}
	if r := g.Run(ctx, pre.Repo, "rev-parse", "--git-path", "info/attributes"); r.OK() {
		p := strings.TrimSpace(string(r.Stdout))
		if !filepath.IsAbs(p) {
			p = filepath.Join(pre.Repo, p)
		}
		if b, err := os.ReadFile(p); err == nil {
			addAttrs(string(b), p)
		}
	}

	names := map[string]bool{}
	for n, c := range cmds {
		if lossyCommand(c) || lossyNameRe.MatchString(n) {
			names[n] = true
		}
	}
	for n := range attrSrc {
		if lossyNameRe.MatchString(n) {
			names[n] = true
		}
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	var out []Warning
	for _, n := range sorted {
		file, where := "git config", "defined in git config"
		if _, ok := cmds[n]; !ok {
			file, where = attrSrc[n], "named by a merge attribute but not defined"
		} else if a, ok := attrSrc[n]; ok {
			where = "defined in git config and named by " + a
		}
		out = append(out, Warning{
			Word: outcome.WarnLossyMergeDriver, File: file, Key: "merge." + n + ".driver",
			detail: fmt.Sprintf("driver %q keeps one side whole (%s); during a rebase ours is the upstream, so the branch's own change is dropped without a conflict stop", n, where),
		})
	}
	return out
}
