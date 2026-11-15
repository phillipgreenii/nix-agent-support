// Package conflict resolves the conflicts of a rebase that git's own merge
// drivers left: it matches each conflicted path to a conflict rule, runs the
// rule's resolver and verify command, checks for leftover conflict markers,
// commits the resolution and continues the rebase (design section 10).
package conflict

import (
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/glob"
)

// Match is the paths one rule resolves.
type Match struct {
	Rule  *config.Rule
	Paths []string // sorted
}

// MatchRules takes, for each conflicted path, the first rule whose glob
// matches (cfg.Rules is already in layer order), else conflict_default. It
// returns the matches in rule order (conflict_default last), and the paths
// that have no rule at all.
func MatchRules(cfg *config.Config, paths []string) (matches []Match, none []string) {
	byRule := map[*config.Rule][]string{}
	for _, p := range paths {
		var hit *config.Rule
		for i := range cfg.Rules {
			if glob.Match(cfg.Rules[i].Glob, p) {
				hit = &cfg.Rules[i]
				break
			}
		}
		if hit == nil {
			hit = cfg.ConflictDefault
		}
		if hit == nil {
			none = append(none, p)
			continue
		}
		byRule[hit] = append(byRule[hit], p)
	}
	add := func(r *config.Rule) {
		if ps, ok := byRule[r]; ok {
			sort.Strings(ps)
			matches = append(matches, Match{Rule: r, Paths: ps})
		}
	}
	for i := range cfg.Rules {
		add(&cfg.Rules[i])
	}
	if cfg.ConflictDefault != nil {
		add(cfg.ConflictDefault)
	}
	sort.Strings(none)
	return matches, none
}

// HasMarkers reports whether content holds a conflict block: a line starting
// with seven "<" and a space, and later a line starting with seven ">" and a
// space. A lone line of seven "=" is not a marker.
func HasMarkers(content []byte) bool {
	open := false
	for _, l := range strings.Split(string(content), "\n") {
		switch {
		case !open && strings.HasPrefix(l, "<<<<<<< "):
			open = true
		case open && strings.HasPrefix(l, ">>>>>>> "):
			return true
		}
	}
	return false
}
