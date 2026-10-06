package attention

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// View is the projection of one entity the rules read: the stored
// interpretation row (decoded) plus, lazily and only for the rules that need
// them, the narrow facts the interpretation does not carry. The projector
// MUST NOT re-implement interpretation: panels, approvals and match reasons
// are read as stored, and the facts it derives go through internal/interpret's
// own helpers.
type View struct {
	Repo string
	Type string
	ID   string

	Ownership    string
	Panel        string
	Approvals    interpret.Approvals
	MatchReasons []string
	// Degraded is true when the stored interpretation is degraded; no rule
	// raises on a degraded view (INV-ATTNEVAL-6).
	Degraded bool
	// AsOf is the interpretation's as-of time (zero when unparsable).
	AsOf time.Time

	// Entity is the stored entity row; nil when the entity has none.
	Entity *store.Entity

	cfg      *config.Config
	factsOK  bool
	factsErr bool
	facts    interpret.PRAttentionFacts
}

// Ref is the view's "<type>:<id>".
func (v *View) Ref() string { return Ref(v.Type, v.ID) }

// OwnPR reports whether the PR is the operator's own (mine or co-owned).
func (v *View) OwnPR() bool { return interpret.Ownership(v.Ownership).ActsAsMine() }

// PRFacts returns the facts derived from the stored entity facts, computed at
// most once. ok is false when the entity has no stored facts or they do not
// decode; a rule then raises nothing.
func (v *View) PRFacts() (interpret.PRAttentionFacts, bool) {
	if v.factsOK {
		return v.facts, true
	}
	if v.factsErr || v.Entity == nil {
		return interpret.PRAttentionFacts{}, false
	}
	f, err := interpret.DerivePRAttentionFacts(v.Entity.Facts, v.cfg)
	if err != nil {
		v.factsErr = true
		return interpret.PRAttentionFacts{}, false
	}
	v.facts, v.factsOK = f, true
	return f, true
}

func (v *View) candidate(kind string, sev Severity, reason string) Candidate {
	return Candidate{Kind: kind, Type: v.Type, ID: v.ID, Severity: sev, Reason: reason, Since: v.AsOf}
}

// Project builds one View per stored interpretation row of repo, in the
// store's deterministic order. An entity the store marks inactive (no longer
// watched) is not projected. A row whose JSON columns do not decode is
// projected as degraded rather than dropped, so it raises nothing but still
// has an explainable trace.
func Project(r Reader, repo string, cfg *config.Config) ([]*View, error) {
	rows, err := r.ListInterpretations()
	if err != nil {
		return nil, fmt.Errorf("attention: read interpretations: %w", err)
	}
	var out []*View
	for _, row := range rows {
		if row.Repo != repo {
			continue
		}
		v := &View{
			Repo: row.Repo, Type: row.EntityType, ID: row.EntityID,
			Ownership: row.Ownership, Panel: row.Panel, Degraded: row.Degraded,
			cfg: cfg,
		}
		if t, perr := time.Parse(time.RFC3339, row.AsOf); perr == nil {
			v.AsOf = t
		}
		if row.Approvals != "" {
			if jerr := json.Unmarshal([]byte(row.Approvals), &v.Approvals); jerr != nil {
				v.Degraded = true
			}
		}
		if row.MatchReasons != "" {
			if jerr := json.Unmarshal([]byte(row.MatchReasons), &v.MatchReasons); jerr != nil {
				v.Degraded = true
			}
		}
		ent, found, gerr := r.GetEntity(row.Repo, row.EntityType, row.EntityID)
		if gerr != nil {
			return nil, fmt.Errorf("attention: read entity %s:%s: %w", row.EntityType, row.EntityID, gerr)
		}
		if found {
			if ent.Inactive {
				continue
			}
			v.Entity = &ent
		}
		out = append(out, v)
	}
	return out, nil
}
