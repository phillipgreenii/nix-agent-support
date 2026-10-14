package executor

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/parknote"
)

// parkNote renders the comment written on a bead the handler parks as human
// because its session is gone (bead pg2-78mxs): the trigger, the session and
// role, the drain worktree and branch with their commit state, and the
// statement that no person-question was identified. Worktree discovery is
// best effort and bounded (internal/parknote): it can degrade the note but
// never block or fail the park.
func (r *ccpoolRun) parkNote(ctx context.Context, d DispatchContext, name, trigger string, detail []string) string {
	r.sigMu.Lock()
	cwd := r.row.CWD
	r.sigMu.Unlock()
	return parknote.Build(ctx, parknote.Input{
		Trigger: trigger,
		Detail:  detail,
		Session: name,
		Display: d.Role.DisplayName(r.deps.Cfg.SessionPrefix, d.Item.ID),
		Role:    d.Role.Name,
		Bead:    d.Item.ID,
		Dirs:    []string{r.deps.Cfg.RepoRoot, cwd},
		Git:     r.deps.ParkGit,
	})
}

// deathDetail is the exit information the handler already holds for a session
// that died: ccpool's recorded close reason, the last observed row facts and
// the captured failure signature (redacted and bounded by failsig).
func (r *ccpoolRun) deathDetail() []string {
	sig := r.captureSignature()
	r.sigMu.Lock()
	ex := r.exit
	r.sigMu.Unlock()
	var out []string
	reason := ex.CloseReason
	if reason == "" {
		reason = "none recorded"
	}
	out = append(out, "ccpool close reason: "+reason)
	switch {
	case !ex.Observed:
		out = append(out, "last observed session row: none")
	case !ex.Present:
		out = append(out, "last observed session row: absent from the pool list")
	default:
		out = append(out, fmt.Sprintf("last observed session row: state %s, live %t", ex.State, ex.Live))
	}
	line := "failure signature: " + string(sig.Signature)
	if sig.Evidence != "" {
		line += " (evidence: " + sig.Evidence + ")"
	}
	return append(out, line)
}
