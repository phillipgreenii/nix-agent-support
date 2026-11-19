package alertrules

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// pg2-e0myo4: the pg-router-queue-stalled role join has an explicit, named exemption
// list for runner roles (grafana/alerting/queue-stalled-exempt-roles.json). A runner
// (repo-verify, repo-sync) answers exit 9 "not now" and is re-offered, so a non-empty
// queue with nothing completing is its routine, and the rule would page it. The
// deployment's check reads the list: every (role, type) pair a daemon config binds is
// covered by the pg_router_binding_info join or exempt here. These tests pin the list,
// that the rule excludes exactly the listed roles, and the classification the deployment applies.

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

// stalledExcludedRoles is the set of roles the rule drops from the type-to-role join:
// the roles of the `pg_router_binding_info{role!~"a|b"}` selector, a plain alternation.
func stalledExcludedRoles(t *testing.T) []string {
	t.Helper()
	expr := ruleExpr(t, ruleBlock(t, "pg-router-queue-stalled"))
	m := regexp.MustCompile(`pg_router_binding_info\{role!~"([^"]+)"\}`).FindStringSubmatch(expr)
	if m == nil {
		t.Fatalf("queue-stalled has no pg_router_binding_info{role!~...} exclusion: %q", expr)
	}
	return strings.Split(m[1], "|")
}

// coverage is the classification the deployment applies to one bound pair: covered by
// the join (its role is not excluded), exempt (excluded and on the named list), or
// uncovered (excluded without being listed; a failed check). excluded is the rule's
// exclusion set; nil means the rule excludes nothing.
func coverage(excluded []string, exempt exemptList, role, typ string) string {
	if !slices.Contains(excluded, role) {
		return "covered"
	}
	for _, e := range exempt.ExemptRoles {
		if e.Role == role && slices.Contains(e.Types, typ) {
			return "exempt"
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
		if got := coverage([]string{want.role}, l, want.role, want.typ); got != "exempt" {
			t.Errorf("(%s, %s) is %s, want exempt", want.role, want.typ, got)
		}
	}
}

// The rule's exclusion set is EXACTLY the exempt roles of the list (so a role cannot be
// excluded without a reason and a reason, or be listed and still paged), and no
// exempt type is one a non-exempt bound role binds.
func TestStalledExcludedRolesAreTheExemptRoles(t *testing.T) {
	l := loadExempt(t)
	var want []string
	for _, e := range l.ExemptRoles {
		want = append(want, e.Role)
		for typ, role := range stalledTypes {
			if role == e.Role || slices.Contains(e.Types, typ) {
				t.Errorf("exempt entry %q/%v collides with the bound pair %s/%s", e.Role, e.Types, role, typ)
			}
		}
	}
	got := stalledExcludedRoles(t)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("rule excludes roles %v, the exemption list names %v", got, want)
	}
}

// A runner role is exempt and a non-runner role is not: every non-runner pair the
// daemon binds is covered (and so is not exempt), a runner pair is exempt, an
// unlisted pair of an excluded role is uncovered, and with the list withdrawn the
// runner pairs are uncovered.
func TestStalledCoverageClassification(t *testing.T) {
	l := loadExempt(t)
	excluded := stalledExcludedRoles(t)
	for typ, role := range stalledTypes {
		if got := coverage(excluded, l, role, typ); got != "covered" {
			t.Errorf("non-runner pair (%s, %s) is %s, want covered", role, typ, got)
		}
	}
	for _, p := range []struct{ role, typ, want string }{
		{"repo-verify", "repo.tip", "exempt"},
		{"repo-sync", "repo.sync", "exempt"},
		// The exemption is per (role, type): an excluded role with another type is not exempt.
		{"repo-verify", "repo.sync", "uncovered"},
		{"repo-sync", "repo.tip", "uncovered"},
		{"repo-verify", "other.thing", "uncovered"},
		// A role the rule does not exclude is covered whatever its type.
		{"workspace-build", "workspace.tips", "covered"},
		{"some-new-role", "some-new.type", "covered"},
	} {
		if got := coverage(excluded, l, p.role, p.typ); got != p.want {
			t.Errorf("(%s, %s) is %s, want %s", p.role, p.typ, got, p.want)
		}
		if p.want == "exempt" {
			if got := coverage(excluded, exemptList{}, p.role, p.typ); got != "uncovered" {
				t.Errorf("(%s, %s) is %s with an empty list, want uncovered", p.role, p.typ, got)
			}
		}
	}
}
