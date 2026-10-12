package effectpolicy

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/pathspec"
)

// ConfigExecSourcePolicy judges the two effect kinds packet tc-o14i5.3.5
// (design item e, P13) adds: EffectConfigSource (a file or flake reference the
// tool will load and obey) and EffectExecProgram (a command or path the tool
// will run). Per P14 the verdict comes only from here; the schemas only
// record the facts. Nothing here opens or runs the named source (P9): it is
// judged by WHERE it lives and, for a command line, by the effects of the
// child the interpreter already emitted.
//
//   - a dynamic source (runtime expansion, stdin) => Unknown (P3);
//   - a source path that is a secret/forbidden read => Forbidden: the tool
//     would load its contents;
//   - a source path inside the R2 repo-trust boundary (pathspec.
//     InsideRepoTrust: the project root, its worktrees, same-workforest
//     siblings) => Permitted - it is checkout state, trusted per
//     docs/adr/0053-ceta-threat-model.md "2. What is trusted vs. what is
//     screened", the same footing TrustedCheckoutExec gives `go test`;
//   - a source OUTSIDE the boundary, a non-local flake reference, or program
//     text the engine has no interpreter for (Dialect "") => Unknown: code
//     the engine cannot see never approves (P3);
//   - an exec command line (Dialect "shell") => Permitted AS AN EFFECT,
//     because the same text is judged as its own child; if that child's
//     effects violate policy the fold rejects, if it cannot be seen it
//     abstains.
type ConfigExecSourcePolicy struct{}

// Name implements Policy.
func (ConfigExecSourcePolicy) Name() string { return "config-exec-source" }

// Judge implements Policy.
func (ConfigExecSourcePolicy) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	switch e.Kind {
	case cmddesc.EffectConfigSource:
		if e.Dynamic {
			return Finding{Verdict: Unknown, Reason: "config source " + e.Path + " is a runtime value (" + orDefault(e.Detail, "expansion") + ")"}, true
		}
		p := e.Path
		if e.Detail == cmddesc.FlakeRefDetail {
			local, ok := localFlakeRefPath(p)
			if !ok {
				return Finding{Verdict: Unknown, Reason: "flake reference " + p + " is not a local path (remote, registry or scheme reference): code the engine cannot see"}, true
			}
			p = local
		}
		return judgeSourcePath("config source", p, ctx), true
	case cmddesc.EffectExecProgram:
		if e.Dynamic {
			return Finding{Verdict: Unknown, Reason: "exec program " + e.Program + " is a runtime value"}, true
		}
		switch e.Dialect {
		case cmddesc.ExecDialectShell:
			return Finding{Verdict: Permitted, Reason: "exec command line is judged as its own child invocation"}, true
		case cmddesc.ExecDialectPath:
			if e.Program == "" {
				return Finding{Verdict: Unknown, Reason: "exec path is empty"}, true
			}
			return judgeSourcePath("exec source", e.Program, ctx), true
		default:
			return Finding{Verdict: Unknown, Reason: "exec program " + e.Program + " is text the engine cannot interpret"}, true
		}
	}
	return Finding{}, false
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// judgeSourcePath applies the secret check and the R2 boundary check to a
// source path.
func judgeSourcePath(what, p string, ctx PolicyContext) Finding {
	if ctx.PathEval == nil {
		return Finding{Verdict: Unknown, Reason: what + " " + p + ": no path evaluator to locate it"}
	}
	if reason, secret := secretRead(p, ctx); secret {
		return Finding{Verdict: Forbidden, Reason: what + " " + p + " is not readable: " + reason}
	}
	abs := resolvePathAccessAbs(ctx.PathEval, p)
	root := ctx.PathEval.ProjectRoot()
	if abs == "" || root == "" || !ctx.PathEval.ProjectRootGrantsZone() {
		return Finding{Verdict: Unknown, Reason: what + " " + p + ": no project root to judge the trust boundary against"}
	}
	if inside, reason := pathspec.InsideRepoTrust(nil, root, abs); inside {
		return Finding{Verdict: Permitted, Reason: what + " " + p + " is inside the repo-trust boundary (" + reason + ")"}
	} else {
		return Finding{Verdict: Unknown, Reason: what + " " + p + " is outside the repo-trust boundary (" + reason + "): code the engine cannot see"}
	}
}

// localFlakeRefPath extracts the filesystem path of a flake reference that is
// a bare relative or absolute PATH (optionally followed by `#attr` and/or
// `?query`): ".", "./sub", "../x#shell", "/abs/dir#a". Anything with a scheme
// (`github:`, `git+https:`, `path:`, `flake:`), or a registry shorthand
// (`nixpkgs#hello`, which has no path-like first component), is non-local.
func localFlakeRefPath(ref string) (string, bool) {
	base := ref
	if i := strings.IndexAny(base, "#?"); i >= 0 {
		base = base[:i]
	}
	if base == "" {
		return ".", true // `#attr`: the current directory's flake
	}
	if strings.Contains(base, ":") {
		return "", false
	}
	if base == "." || base == ".." || strings.HasPrefix(base, "./") || strings.HasPrefix(base, "../") || strings.HasPrefix(base, "/") {
		return base, true
	}
	return "", false
}
