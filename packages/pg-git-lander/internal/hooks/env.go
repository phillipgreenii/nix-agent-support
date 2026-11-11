package hooks

import (
	"sort"
	"strings"
)

// EnvPrefix is the namespace the tool sets for hooks, resolvers and verify
// commands. The tool's own inputs (PGL_*) deliberately do not use it, so a
// tool run started from a hook does not inherit the outer operation's roles.
const EnvPrefix = "PG_GIT_LANDER_"

// ContextPair is one --context key=value pair.
type ContextPair struct{ Key, Value string }

// Facts are the operation facts the command environment is built from
// (design section 13.2).
type Facts struct {
	// Operation is "rebase" or "land".
	Operation string
	OpID      string
	Repo      string
	// GitCommonDir is the git common dir.
	GitCommonDir        string
	WorkWorktree        string
	SourceWorktree      string
	DestinationWorktree string // land only
	WorkBranch          string // empty at before
	Branch, BranchTip   string
	Target, TargetTip   string // land only
	Onto                string
	Result              string // after_rebase, before_move and after
	BaseMoved           bool
	Context             []ContextPair
}

// ContextKey turns a --context key into the KEY of
// PG_GIT_LANDER_CONTEXT_<KEY>: upper-cased, every character other than a
// letter or digit becoming "_".
func ContextKey(k string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(k) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Vars returns the PG_GIT_LANDER_* variables for hook point (or "conflict"
// for a resolver or verify command). A later --context pair with the same key
// wins.
func (f Facts) Vars(point string) map[string]string {
	v := map[string]string{
		"OPERATION":       f.Operation,
		"OP_ID":           f.OpID,
		"HOOK":            point,
		"REPO":            f.Repo,
		"GIT_COMMON_DIR":  f.GitCommonDir,
		"WORK_WORKTREE":   f.WorkWorktree,
		"SOURCE_WORKTREE": f.SourceWorktree,
		"WORK_BRANCH":     f.WorkBranch,
		"BRANCH":          f.Branch,
		"BRANCH_START":    f.BranchTip,
		"ONTO":            f.Onto,
		"BASE_MOVED":      boolWord(f.BaseMoved),
	}
	if f.Operation == "land" {
		v["DESTINATION_WORKTREE"] = f.DestinationWorktree
		v["TARGET"] = f.Target
		v["TARGET_TIP"] = f.TargetTip
	}
	if point != PointBefore {
		v["RESULT"] = f.Result
	}
	out := make(map[string]string, len(v)+len(f.Context))
	for k, val := range v {
		out[EnvPrefix+k] = val
	}
	for _, c := range f.Context {
		out[EnvPrefix+"CONTEXT_"+ContextKey(c.Key)] = c.Value
	}
	return out
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// BuildEnv returns the complete child environment: base, then the
// PG_GIT_LANDER_* variables, then the entry's own env (which wins), with each
// name once. added lists the names (never the values) of the variables that
// were added on top of base, sorted.
func BuildEnv(base []string, vars, extra map[string]string) (env []string, added []string) {
	merged := map[string]string{}
	for k, v := range vars {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	for k := range merged {
		added = append(added, k)
	}
	sort.Strings(added)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, over := merged[name]; over {
			continue
		}
		env = append(env, kv)
	}
	for _, k := range added {
		env = append(env, k+"="+merged[k])
	}
	return env, added
}
