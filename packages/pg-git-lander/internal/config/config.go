// Package config loads pg-git-lander's configuration: the XDG file, the repo
// file read from the right git tip, the layering key by key, the trust rule
// for commands named in a repo file, ignore_repo_config, the primary branch
// and the digest of the configuration in effect (design section 12).
//
// Loading has two stages. LoadXDG runs before any lock and reads only the
// built-in defaults and the XDG file. Pre.Load runs after locking and adds the
// repo file, read with git from a ref's tip and never from a working tree;
// calling it again is the reload at every return after a race and at --continue.
package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

// Built-in defaults (design section 12.4).
const (
	DefaultLeaseTTL          = 30 * time.Second
	DefaultLockWait          = 60 * time.Second
	DefaultMaxRaceRetries    = 2
	DefaultMaxConflictRounds = 10
	DefaultAutofixAttempts   = 2
	FallbackPrimary          = "main"

	XDGFileName  = "config.toml"
	RepoFileName = ".pg-git-lander.toml"
)

// Source names one place configuration came from.
type Source struct {
	// Kind is "xdg" or "repo-file".
	Kind string `json:"kind"`
	// Path is the XDG file's path, or the repo file's path within the tree.
	Path string `json:"path"`
	// Tip is, for the repo file, the commit it was read from.
	Tip string `json:"tip,omitempty"`
	// Ref is, for the repo file, the ref that was resolved to Tip.
	Ref string `json:"ref,omitempty"`
	// Present is false when the file does not exist.
	Present bool `json:"present"`
}

// Warning is a warning carried by an exit-0 outcome.
type Warning struct {
	// detail is the human explanation, shown by config check.
	detail string
	Word   outcome.Warning `json:"word"`
	File   string          `json:"file"`
	Line   int             `json:"line"`
	Key    string          `json:"key"`
}

// Lock holds lock.* keys.
type Lock struct {
	LeaseTTL time.Duration `json:"lease_ttl"`
	Wait     time.Duration `json:"wait"`
}

// Config is the configuration in effect.
type Config struct {
	// Primary is the configured primary branch, empty when none is
	// configured; use Pre.ResolvePrimary for the resolved branch.
	Primary           string                       `json:"primary"`
	Lock              Lock                         `json:"lock"`
	RunGitHooks       bool                         `json:"run_git_hooks"`
	MaxRaceRetries    int                          `json:"max_race_retries"`
	MaxConflictRounds int                          `json:"max_conflict_rounds"`
	AutofixAttempts   int                          `json:"autofix_attempts"`
	Hooks             map[string]map[string][]Hook `json:"hooks"`
	// Rules are the conflict rules in matching order: the XDG [[repo]]
	// entry's, then the repo file's, then the XDG [defaults]'.
	Rules []Rule `json:"rules"`
	// ConflictDefault is the rule for a path no rule matches, or nil.
	ConflictDefault *Rule `json:"conflict_default"`

	TrustRepoConfig  bool `json:"trust_repo_config"`
	IgnoreRepoConfig bool `json:"ignore_repo_config"`

	// Sources and Warnings are not part of the digest.
	Sources  []Source  `json:"-"`
	Warnings []Warning `json:"-"`
	// Digest identifies the configuration in effect; it is recorded in the
	// operation record and compared on resume.
	Digest string `json:"-"`
}

// HooksFor returns the hook list of operation at point (empty when none).
func (c *Config) HooksFor(operation, point string) []Hook { return c.Hooks[operation][point] }

// Error is a configuration failure that maps to an outcome: the word
// config-error (exit 2) for the XDG file, or a refusal for the repo file.
type Error struct {
	Word    outcome.Word
	Reason  outcome.RefusalReason // set when Word is outcome.Refused
	Loc     *outcome.ConfigErr    // file, line, key and layer, when known
	Message string
	Hint    string
}

func (e *Error) Error() string {
	if e.Loc != nil {
		return fmt.Sprintf("%s:%d: %s: %s (layer %s)", e.Loc.File, e.Loc.Line, e.Loc.Key, e.Message, e.Loc.Layer)
	}
	return e.Message
}

// Loader loads configuration.
type Loader struct {
	Git   *gitexec.Git
	Paths seams.ConfigPaths
}

// XDGPath is the path of the XDG file.
func (l *Loader) XDGPath() string { return filepath.Join(l.Paths.Dir(), XDGFileName) }

// Pre is the result of the first stage: everything known before any lock.
type Pre struct {
	l *Loader
	// RepoDir is the directory the operation runs in; Repo is the main
	// worktree (the repo).
	RepoDir string
	Repo    string

	xdgPath  string
	xdgFound bool
	defaults *vals
	entry    *vals // the matching [[repo]] entry, or nil

	// Lock, Primary-config and the two repo flags are final here: the repo
	// file cannot set them.
	Lock             Lock
	TrustRepoConfig  bool
	IgnoreRepoConfig bool
	primary          string
}

// LoadXDG is the first stage: built-in defaults and the XDG file only, for
// the repository containing repoDir. A missing XDG file is not an error.
func (l *Loader) LoadXDG(ctx context.Context, repoDir string) (*Pre, error) {
	pre := &Pre{l: l, RepoDir: repoDir, xdgPath: l.XDGPath(), defaults: &vals{}}
	wts, err := l.Git.Worktrees(ctx, repoDir)
	if err != nil || len(wts) == 0 {
		return nil, fmt.Errorf("config: listing worktrees of %s: %v", repoDir, err)
	}
	pre.Repo = wts[0].Path

	src, err := os.ReadFile(pre.xdgPath)
	switch {
	case err == nil:
		pre.xdgFound = true
	case errors.Is(err, os.ErrNotExist):
		src = nil
	default:
		return nil, &Error{
			Word: outcome.ConfigError, Message: err.Error(),
			Loc: &outcome.ConfigErr{File: pre.xdgPath, Layer: "xdg"},
		}
	}
	if pre.xdgFound {
		doc, errs := parseXDG(src)
		if len(errs) > 0 {
			return nil, xdgError(pre.xdgPath, errs[0])
		}
		pre.defaults = doc.defaults
		common, err := l.Git.CommonDir(ctx, repoDir)
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		common = resolve(common)
		for _, e := range doc.repos {
			rc, err := l.Git.CommonDir(ctx, e.root)
			if err != nil {
				continue // not a clone (config check reports it)
			}
			if resolve(rc) == common {
				pre.entry = e
				break
			}
		}
	}

	pre.Lock = Lock{LeaseTTL: DefaultLeaseTTL, Wait: DefaultLockWait}
	for _, v := range []*vals{pre.defaults, pre.entry} {
		if v == nil {
			continue
		}
		if v.leaseTTL != nil {
			pre.Lock.LeaseTTL = *v.leaseTTL
		}
		if v.wait != nil {
			pre.Lock.Wait = *v.wait
		}
		if v.primary != nil {
			pre.primary = *v.primary
		}
	}
	if pre.entry != nil {
		pre.TrustRepoConfig = pre.entry.trust != nil && *pre.entry.trust
		pre.IgnoreRepoConfig = pre.entry.ignore != nil && *pre.entry.ignore
	}
	return pre, nil
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func xdgError(file string, is issue) *Error {
	return &Error{
		Word: outcome.ConfigError, Message: is.msg,
		Loc: &outcome.ConfigErr{File: file, Line: is.line, Key: is.key, Layer: "xdg"},
	}
}

// XDGSource describes the XDG file as a source in effect.
func (p *Pre) XDGSource() Source {
	return Source{Kind: "xdg", Path: p.xdgPath, Present: p.xdgFound}
}

// ResolvePrimary returns the primary branch: the XDG configuration's primary,
// else the branch refs/remotes/origin/HEAD names, else main.
func (p *Pre) ResolvePrimary(ctx context.Context) string {
	if p.primary != "" {
		return p.primary
	}
	r := p.l.Git.Run(ctx, p.Repo, "symbolic-ref", "-q", "refs/remotes/origin/HEAD")
	if r.OK() {
		ref := strings.TrimSpace(string(r.Stdout))
		if b, ok := strings.CutPrefix(ref, "refs/remotes/origin/"); ok && b != "" {
			return b
		}
	}
	return FallbackPrimary
}

// LandTip is the ref a land reads the repo file from: TARGET's tip.
func LandTip(target string) string { return "refs/heads/" + target }

// RebaseTip is the ref a rebase reads the repo file from: the local primary
// branch's tip (never BRANCH or ONTO).
func (p *Pre) RebaseTip(ctx context.Context) string { return "refs/heads/" + p.ResolvePrimary(ctx) }

// Load is the second stage: it reads the repo file from ref's tip (use
// LandTip or RebaseTip; an empty ref means no repo file) and layers
// everything. Call it again for the reload after a race and at --continue.
func (p *Pre) Load(ctx context.Context, ref string) (*Config, error) {
	cfg := &Config{Sources: []Source{p.XDGSource()}}
	var file *vals
	if ref != "" && !p.IgnoreRepoConfig {
		var err error
		file, err = p.readRepoFile(ctx, ref, cfg)
		if err != nil {
			return nil, err
		}
	}
	p.layer(cfg, file)
	cfg.Digest = digest(cfg)
	return cfg, nil
}

func (p *Pre) readRepoFile(ctx context.Context, ref string, cfg *Config) (*vals, error) {
	g := p.l.Git
	r := g.Run(ctx, p.Repo, "ls-tree", "--full-tree", ref, "--", RepoFileName)
	if !r.OK() {
		return nil, fmt.Errorf("config: reading %s from %s: %s", RepoFileName, ref, strings.TrimSpace(string(r.Stderr)))
	}
	src := Source{Kind: "repo-file", Path: RepoFileName, Ref: ref}
	if t := g.Run(ctx, p.Repo, "rev-parse", "--verify", "-q", ref+"^{commit}"); t.OK() {
		src.Tip = strings.TrimSpace(string(t.Stdout))
	}
	line := strings.TrimSpace(string(r.Stdout))
	if line == "" {
		cfg.Sources = append(cfg.Sources, src)
		return nil, nil
	}
	src.Present = true
	cfg.Sources = append(cfg.Sources, src)
	display := fmt.Sprintf("%s:%s", ref, RepoFileName)
	meta, _, _ := strings.Cut(line, "\t")
	fields := strings.Fields(meta)
	if len(fields) != 3 || fields[1] != "blob" || fields[0] == "120000" {
		return nil, p.repoErr(display, issue{msg: "must be a regular file"})
	}
	b := g.Run(ctx, p.Repo, "cat-file", "blob", fields[2])
	if !b.OK() {
		return nil, fmt.Errorf("config: reading blob %s: %s", fields[2], strings.TrimSpace(string(b.Stderr)))
	}
	v, errs, unknown := parseRepoFile(b.Stdout)
	if len(errs) > 0 {
		return nil, p.repoErr(display, errs[0])
	}
	for _, u := range unknown {
		cfg.Warnings = append(cfg.Warnings, Warning{Word: outcome.WarnUnknownRepoConfigKey, File: display, Line: u.line, Key: u.key})
	}
	if v.namesCommands() && !p.TrustRepoConfig {
		return nil, &Error{
			Word: outcome.Refused, Reason: outcome.RefUntrustedConfig,
			Message: fmt.Sprintf("%s names commands but the repo is not trusted", display),
			Hint:    p.trustHint(),
		}
	}
	return v, nil
}

func (p *Pre) repoErr(file string, is issue) *Error {
	return &Error{
		Word: outcome.Refused, Reason: outcome.RefRepoConfigError, Message: is.msg,
		Loc:  &outcome.ConfigErr{File: file, Line: is.line, Key: is.key, Layer: LayerRepoFile},
		Hint: outcome.Refusals[outcome.RefRepoConfigError].Hint,
	}
}

// trustHint quotes the XDG line to add.
func (p *Pre) trustHint() string {
	if p.entry != nil {
		return fmt.Sprintf("In %s set trust_repo_config = true in the [[repo]] entry for %s.", p.xdgPath, p.entry.root)
	}
	return fmt.Sprintf("Add to %s:\n[[repo]]\nroot = %q\ntrust_repo_config = true", p.xdgPath, p.Repo)
}

// layer applies, key by key: built-in defaults, XDG [defaults], the repo
// file, the XDG [[repo]] entry.
func (p *Pre) layer(cfg *Config, file *vals) {
	cfg.Lock = p.Lock
	cfg.RunGitHooks = true
	cfg.MaxRaceRetries = DefaultMaxRaceRetries
	cfg.MaxConflictRounds = DefaultMaxConflictRounds
	cfg.AutofixAttempts = DefaultAutofixAttempts
	cfg.Hooks = map[string]map[string][]Hook{}
	cfg.Rules = []Rule{}
	cfg.Primary = p.primary
	cfg.TrustRepoConfig = p.TrustRepoConfig
	cfg.IgnoreRepoConfig = p.IgnoreRepoConfig

	for _, v := range []*vals{p.defaults, file, p.entry} {
		if v == nil {
			continue
		}
		if v.runGitHooks != nil {
			cfg.RunGitHooks = *v.runGitHooks
		}
		if v.maxRace != nil {
			cfg.MaxRaceRetries = *v.maxRace
		}
		if v.maxConflict != nil {
			cfg.MaxConflictRounds = *v.maxConflict
		}
		if v.autofix != nil {
			cfg.AutofixAttempts = *v.autofix
		}
		for op, pts := range v.hooks {
			if cfg.Hooks[op] == nil {
				cfg.Hooks[op] = map[string][]Hook{}
			}
			for pt, hs := range pts {
				cfg.Hooks[op][pt] = hs // replaced as a whole
			}
		}
		if v.conflictDefault != nil {
			cfg.ConflictDefault = v.conflictDefault
		}
	}
	// Rule order (design section 10.2): XDG [[repo]] entry, repo file, XDG
	// [defaults].
	for _, v := range []*vals{p.entry, file, p.defaults} {
		if v != nil {
			cfg.Rules = append(cfg.Rules, v.rules...)
		}
	}
}

func digest(cfg *Config) string {
	b, err := json.Marshal(cfg) // map keys are sorted: deterministic
	if err != nil {
		panic(err) // Config holds only marshalable types
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
