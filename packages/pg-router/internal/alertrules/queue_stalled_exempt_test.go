package alertrules

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"testing"
)

// pg2-e0myo4: the pg-router-queue-stalled role map has an explicit, named exemption
// list for runner roles (grafana/alerting/queue-stalled-exempt-roles.json). A runner
// (repo-verify, repo-sync) answers exit 9 "not now" and is re-offered, so a non-empty
// queue with nothing completing is its routine, and the rule would page it. The
// deployment's check reads the list: every (role, type) pair a daemon config binds MUST
// be covered by a clause of the chain or exempt here. These tests pin the list, the
// disjointness of the two sets, and the classification the deployment applies.

const exemptFile = "../../grafana/alerting/queue-stalled-exempt-roles.json"

type exemptRole struct {
	Role      string   `json:"role"`
	Types     []string `json:"types"`
	Reason    string   `json:"reason"`
	CoveredBy []string `json:"coveredBy"`
}

type exemptList struct {
	Rule        string       `json:"rule"`
	Doc         string       `json:"doc"`
	ExemptRoles []exemptRole `json:"exemptRoles"`
}

func loadExempt(t *testing.T) exemptList {
	t.Helper()
	b, err := os.ReadFile(exemptFile)
	if err != nil {
		t.Fatal(err)
	}
	var l exemptList
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		t.Fatalf("%s: %v", exemptFile, err)
	}
	return l
}

type chainClause struct{ role, typeRE string }

func stalledClauses(t *testing.T) []chainClause {
	t.Helper()
	expr := ruleExpr(t, ruleBlock(t, "pg-router-queue-stalled"))
	var out []chainClause
	for _, c := range regexp.MustCompile(`, "role", "([^"]+)", "type", "([^"]+)"\)`).FindAllStringSubmatch(expr, -1) {
		out = append(out, chainClause{c[1], c[2]})
	}
	return out
}

// coverage is the classification the deployment applies to one bound pair: covered by
// a clause of the chain, exempt by the named list, or uncovered (a failed check).
func coverage(clauses []chainClause, exempt exemptList, role, typ string) string {
	for _, c := range clauses {
		if c.role == role && regexp.MustCompile("^(?:"+c.typeRE+")$").MatchString(typ) {
			return "covered"
		}
	}
	for _, e := range exempt.ExemptRoles {
		if e.Role != role {
			continue
		}
		for _, et := range e.Types {
			if et == typ {
				return "exempt"
			}
		}
	}
	return "uncovered"
}

func TestStalledExemptListShape(t *testing.T) {
	l := loadExempt(t)
	if l.Rule != "pg-router-queue-stalled" {
		t.Errorf("rule = %q, want pg-router-queue-stalled", l.Rule)
	}
	if len(l.ExemptRoles) == 0 {
		t.Fatal("the exemption list is empty; the runner roles repo-verify and repo-sync are exempt")
	}
	seenRole, seenType := map[string]bool{}, map[string]bool{}
	for _, e := range l.ExemptRoles {
		if e.Role == "" || len(e.Types) == 0 || e.Reason == "" || len(e.CoveredBy) == 0 {
			t.Errorf("entry %+v needs a role, types, a reason and the rules that still watch it", e)
		}
		if seenRole[e.Role] {
			t.Errorf("role %q listed twice", e.Role)
		}
		seenRole[e.Role] = true
		for _, typ := range e.Types {
			if seenType[typ] {
				t.Errorf("type %q listed twice", typ)
			}
			seenType[typ] = true
		}
		// Every watching rule exists in alerts.yaml (ruleBlock fails the test if not),
		// and an exempt role is never left unwatched: the sustained-decline rule is there.
		for _, uid := range e.CoveredBy {
			ruleBlock(t, uid)
		}
		if !slices.Contains(e.CoveredBy, "pg-router-role-busy-long") {
			t.Errorf("exempt role %q is not watched by pg-router-role-busy-long, so a sustained exit-9 decline would go unpaged", e.Role)
		}
	}
	for _, want := range []struct{ role, typ string }{{"repo-verify", "repo.tip"}, {"repo-sync", "repo.sync"}} {
		if got := coverage(nil, l, want.role, want.typ); got != "exempt" {
			t.Errorf("(%s, %s) is %s, want exempt", want.role, want.typ, got)
		}
	}
}

// The two sets are disjoint: an exempt role has no clause in the chain (a clause would
// page its exit-9 backlog) and no exempt type is mapped by any clause.
func TestStalledExemptRolesHaveNoClause(t *testing.T) {
	l := loadExempt(t)
	clauses := stalledClauses(t)
	for _, e := range l.ExemptRoles {
		for _, c := range clauses {
			if c.role == e.Role {
				t.Errorf("exempt role %q also has a clause for %q", e.Role, c.typeRE)
			}
			for _, typ := range e.Types {
				if regexp.MustCompile("^(?:" + c.typeRE + ")$").MatchString(typ) {
					t.Errorf("exempt type %q is mapped by the clause for role %q", typ, c.role)
				}
			}
		}
		for _, r := range stalledRoles {
			if r.role == e.Role {
				t.Errorf("exempt role %q is in the modelled role table", e.Role)
			}
		}
		for typ, role := range stalledTypes {
			if role == e.Role || slices.Contains(e.Types, typ) {
				t.Errorf("exempt entry %q/%v collides with the bound pair %s/%s", e.Role, e.Types, role, typ)
			}
		}
	}
}

// A runner role is exempt and a non-runner role is not: every non-runner pair the
// daemon binds is covered by a clause (and so is not exempt), a pair that is neither is
// uncovered, and with the list withdrawn the runner pairs are uncovered, which is the
// failure the deployment check reported before this list existed.
func TestStalledCoverageClassification(t *testing.T) {
	l := loadExempt(t)
	clauses := stalledClauses(t)
	for typ, role := range stalledTypes {
		if got := coverage(clauses, l, role, typ); got != "covered" {
			t.Errorf("non-runner pair (%s, %s) is %s, want covered", role, typ, got)
		}
		if got := coverage(nil, l, role, typ); got != "uncovered" {
			t.Errorf("non-runner pair (%s, %s) is %s without the chain, want uncovered: it is not exempt", role, typ, got)
		}
	}
	for _, p := range []struct{ role, typ, want string }{
		{"repo-verify", "repo.tip", "exempt"},
		{"repo-sync", "repo.sync", "exempt"},
		// The exemption is per (role, type): the role with another type, or another
		// role with the runner's type, is not exempt.
		{"repo-verify", "repo.sync", "uncovered"},
		{"repo-sync", "repo.tip", "uncovered"},
		{"repo-verify", "other.thing", "uncovered"},
		{"workspace-build", "workspace.tips", "uncovered"},
		{"some-new-role", "some-new.type", "uncovered"},
	} {
		if got := coverage(clauses, l, p.role, p.typ); got != p.want {
			t.Errorf("(%s, %s) is %s, want %s", p.role, p.typ, got, p.want)
		}
		if p.want == "exempt" {
			if got := coverage(clauses, exemptList{}, p.role, p.typ); got != "uncovered" {
				t.Errorf("(%s, %s) is %s with an empty list, want uncovered", p.role, p.typ, got)
			}
		}
	}
}
