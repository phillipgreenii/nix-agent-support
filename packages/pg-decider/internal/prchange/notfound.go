package prchange

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// BeadsSource is the source name the beads backend reports for itself, both as
// the `source` of its per-source row in `pg-connector search` and as the
// `source` of every hit it returns. Only a beads row can hold the escalation
// this command raises, so only a beads row counts, and an unrelated source (a
// forge, a mail box, agent sessions) neither blocks the lookup nor matches it.
const BeadsSource = "pg-connector-issue-beads"

// SearchCap is the number of hits at which the beads backend stops listing
// (`bd search` returns at most this many), so a row whose count reaches it can
// hide the escalation the lookup is looking for.
const SearchCap = 100

// beadsDirEnv is the variable the beads backend reads its beads directory from.
const beadsDirEnv = "PG_CONNECTOR_ISSUE_BEADS_DIR"

// EscalationTitle is the title (and so the dedup key) of the escalation raised
// for a PR that no longer exists: a fixed class marker and the PR as
// `<repo>#<n>`. The marker deliberately does not start with the PR reference,
// because a title starting `<repo>#<n>: ` is the shape the decider adopts as
// that PR's anchor.
func EscalationTitle(id string) string { return "pr not found: " + id }

func (r *run) connectorCall(args ...string) Result {
	c := Call{Name: "pg-connector", Args: args}
	if r.o.Config.BeadsDir != "" {
		c.Env = []string{beadsDirEnv + "=" + r.o.Config.BeadsDir}
	}
	return r.call(c)
}

func (r *run) backendFlag() []string {
	if r.o.Config.AgentTrackerBackend == "" {
		return nil
	}
	return []string{"--backend", r.o.Config.AgentTrackerBackend}
}

// callFailure describes a failed call for a failure line.
func callFailure(name string, args []string, res Result) string {
	what := name + " " + strings.Join(args, " ")
	switch {
	case res.StartErr != nil:
		return what + ": " + res.StartErr.Error()
	case res.Signaled:
		return what + " was ended by a signal (signal: killed)"
	}
	var w struct {
		Error *struct{ Code, Message string } `json:"error"`
	}
	if json.Unmarshal([]byte(res.Stdout), &w) == nil && w.Error != nil {
		return fmt.Sprintf("%s: exit %d: %s: %s", what, res.Code, w.Error.Code, w.Error.Message)
	}
	return fmt.Sprintf("%s: exit %d: %s", what, res.Code, firstLine(res.Stderr+"\n"+res.Stdout))
}

// notFound handles a PR the source says is gone (refresh exit 3, class
// not_found). The decider never runs. The condition is never silent:
//
//   - with no open anchor the PR is marked inactive (a pg-desk store write, so
//     it happens in both modes) and counted separately;
//   - with an open anchor, `apply` raises one `human` escalation, deduplicated
//     across every status, and `plan` only reports it (no tracker write).
//
// Every outcome exits ExitHydrateNotFound; a step that cannot be trusted
// (the probe, the lookup, a write) exits as transient, so the router re-runs
// the delivery and the condition is never read as "no match".
func (r *run) notFound(refreshDetail string) int {
	anchorID, open, class, detail := r.openAnchor()
	if class != "" {
		return r.fail(StageHydrate, class, "the open-anchor probe failed", detail, r.showCmd())
	}

	if !open {
		args := []string{"pr", "mark-inactive", r.id}
		res := r.call(Call{Name: "pg-desk", Args: args})
		if res.StartErr != nil || res.Signaled || res.Code != 0 {
			return r.fail(StageHydrate, ClassTransient, "marking the PR inactive failed",
				callFailure("pg-desk", args, res), "pg-desk pr mark-inactive "+r.id)
		}
		return r.fail(StageHydrate, ClassNotFound, "refresh found no such PR",
			fmt.Sprintf("%s is not found at the source (%s) and has no open anchor; it is marked inactive", r.id, refreshDetail), r.showCmd())
	}

	if r.o.Mode == ModePlan {
		return r.fail(StageHydrate, ClassNotFound, "refresh found no such PR",
			fmt.Sprintf("%s is not found at the source (%s) and its anchor %s is open; plan mode records this and writes nothing: an apply run raises the escalation",
				r.id, refreshDetail, anchorID), "pg-pr-change apply "+r.id)
	}

	title := EscalationTitle(r.id)
	hit, found, class, detail := r.findEscalation(title)
	if class != "" {
		return r.fail(StageHydrate, class, "the escalation lookup failed", detail, r.showCmd())
	}
	if found {
		return r.fail(StageHydrate, ClassNotFound, "refresh found no such PR",
			fmt.Sprintf("%s is not found at the source (%s) and its anchor %s is open; already escalated as %s", r.id, refreshDetail, anchorID, hit), r.showCmd())
	}
	created, class, detail := r.createEscalation(title, anchorID, refreshDetail)
	if class != "" {
		return r.fail(StageHydrate, class, "raising the escalation failed", detail, r.showCmd())
	}
	return r.fail(StageHydrate, ClassNotFound, "refresh found no such PR",
		fmt.Sprintf("%s is not found at the source (%s) and its anchor %s is open; escalated as %s", r.id, refreshDetail, anchorID, created), r.showCmd())
}

// openAnchor says whether the PR has an open anchor: the stored links of
// `pg-desk pr show <id> --json` name it, and a live `pg-connector issue show`
// of the linked anchor says whether it is still open. A PR the store does not
// hold has none. A probe that fails is reported as a transient class.
func (r *run) openAnchor() (anchorID string, open bool, class, detail string) {
	res := r.call(Call{Name: "pg-desk", Args: []string{"pr", "show", r.id, "--json"}})
	if res.Code != 0 && !res.Signaled && res.StartErr == nil && strings.Contains(res.Stderr, "does not resolve") {
		return "", false, "", ""
	}
	v, _, detail := viewFromResult(res)
	if v == nil {
		// Whatever went wrong, a probe that cannot answer is never read as "no
		// open anchor": the router re-runs the delivery.
		return "", false, ClassTransient, detail
	}
	a, ok := workitem.BuildIndex(v).Anchor()
	if !ok {
		return "", false, "", ""
	}
	args := append([]string{"issue", "show", a.ID, "--fresh"}, r.backendFlag()...)
	live := r.connectorCall(args...)
	if live.StartErr != nil || live.Signaled || live.Code != 0 {
		return "", false, ClassTransient, callFailure("pg-connector", args, live)
	}
	var w struct {
		Result struct {
			State string `json:"state"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(live.Stdout), &w); err != nil || w.Result.State == "" {
		return "", false, ClassTransient, fmt.Sprintf("pg-connector %s answered no state for the anchor", strings.Join(args, " "))
	}
	return a.ID, !strings.EqualFold(w.Result.State, "closed"), "", ""
}

type searchSource struct {
	Source string `json:"source"`
	Status string `json:"status"`
	Count  int    `json:"count"`
	Reason string `json:"reason"`
}

type searchOutcome struct {
	Sources []searchSource `json:"sources"`
	Groups  []struct {
		Source  string `json:"source"`
		Results []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Source string `json:"source"`
		} `json:"results"`
	} `json:"groups"`
}

// findEscalation looks for an existing escalation across every status, closed
// included, with `pg-connector search <title>` (the beads backend's `bd
// search`: every status, at most SearchCap hits). The judgment is made from the
// per-source rows, never from the merged hit list: every beads row must have
// succeeded with fewer hits than the cap, a degraded or disabled beads row, a
// row at the cap, an unreadable answer and an answer with no beads row at all
// are all a transient class (never "no match"), and rows of other sources are
// ignored. A hit counts only when its source is the beads backend and its title
// equals the escalation's title exactly.
func (r *run) findEscalation(title string) (id string, found bool, class, detail string) {
	args := []string{"search", title}
	res := r.connectorCall(args...)
	if res.StartErr != nil || res.Signaled {
		return "", false, ClassTransient, callFailure("pg-connector", args, res)
	}
	// search prints its outcome on exit 0 (all healthy), 2 (some degraded) and 3
	// (none healthy); the rows say which, so the exit code is not read.
	var out searchOutcome
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return "", false, ClassTransient, callFailure("pg-connector", args, res)
	}
	beads := 0
	for _, s := range out.Sources {
		if s.Source != BeadsSource {
			continue
		}
		beads++
		switch {
		case s.Status != "succeeded":
			return "", false, ClassTransient, fmt.Sprintf("the search of %s is %s (%s), so the lookup cannot be trusted", s.Source, s.Status, s.Reason)
		case s.Count >= SearchCap:
			return "", false, ClassTransient, fmt.Sprintf("the search of %s reached its %d-hit cap, so the escalation could be in the part cut off", s.Source, SearchCap)
		}
	}
	if beads == 0 {
		return "", false, ClassTransient, "the search reported no beads tracker, so the lookup cannot be trusted"
	}
	for _, g := range out.Groups {
		if g.Source != BeadsSource {
			continue
		}
		for _, h := range g.Results {
			if h.Title == title && (h.Source == "" || h.Source == BeadsSource) {
				return h.ID, true, "", ""
			}
		}
	}
	return "", false, "", ""
}

// createEscalation writes the `human`-labeled escalation under the anchor, on
// the tracker the PR's work items live in.
func (r *run) createEscalation(title, anchorID, refreshDetail string) (id, class, detail string) {
	body := fmt.Sprintf("The pull request %s no longer exists at its source (deleted, or its repository was deleted or transferred): "+
		"it cannot be hydrated, so the decider cannot close its work.\nclass: %s\nstage: %s\nsource said: %s\nnext command: %s\n",
		r.id, ClassNotFound, StageHydrate, refreshDetail, r.showCmd())
	args := []string{
		"issue", "create", "--title", title, "--issue-type", "task", "--description", body,
		"--labels", "human", "--parent", anchorID,
	}
	args = append(args, r.backendFlag()...)
	res := r.connectorCall(args...)
	if res.StartErr != nil || res.Signaled || res.Code != 0 {
		return "", ClassTransient, callFailure("pg-connector", args[:2], res)
	}
	var w struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &w); err != nil || w.Result.ID == "" {
		return "", ClassTransient, "pg-connector issue create returned no id"
	}
	return w.Result.ID, "", ""
}
