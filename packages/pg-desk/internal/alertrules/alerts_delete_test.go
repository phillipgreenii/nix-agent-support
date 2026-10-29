package alertrules

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// retiredRuleUIDs are the four sync_error alert rules removed by PR lane refactor
// packet P9b. Their uids must be in the delete-only file, because Grafana keeps a
// file-provisioned rule live after its file drops it.
var retiredRuleUIDs = []string{
	"pg-desk-sync-error-retrying",
	"pg-desk-sync-error-rows",
	"pg-desk-sync-errors",
	"pg-desk-unreconciled-anchor-age",
}

// keptRuleUID has "sync" in its uid but is a keeper: it MUST NOT be deleted.
const keptRuleUID = "pg-desk-sync-stale"

// alertsFile is the part of alerts.yaml the check reads: the rule uids.
type alertsFile struct {
	Groups []struct {
		Rules []struct {
			UID string `yaml:"uid"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

type deleteFile struct {
	APIVersion  int `yaml:"apiVersion"`
	Groups      any `yaml:"groups"`
	DeleteRules []struct {
		OrgID int    `yaml:"orgId"`
		UID   string `yaml:"uid"`
	} `yaml:"deleteRules"`
}

// problems returns one line per way the delete-only file is wrong against the rule
// file: not apiVersion 1, a groups key, an entry without orgId 1, uids that are not
// exactly the retired uids (each once), a retired uid still defined as a rule, or
// the kept rule being deleted or missing.
func problems(deleteYAML, alertsYAML []byte) []string {
	var out []string
	var d deleteFile
	if err := yaml.Unmarshal(deleteYAML, &d); err != nil {
		return []string{"delete file does not parse: " + err.Error()}
	}
	var a alertsFile
	if err := yaml.Unmarshal(alertsYAML, &a); err != nil {
		return []string{"alerts file does not parse: " + err.Error()}
	}
	if d.APIVersion != 1 {
		out = append(out, "delete file is not apiVersion 1")
	}
	if d.Groups != nil {
		out = append(out, "delete file has a groups key")
	}
	var got []string
	for _, e := range d.DeleteRules {
		if e.OrgID != 1 {
			out = append(out, "deleteRules entry without orgId 1: "+e.UID)
		}
		got = append(got, e.UID)
		if e.UID == keptRuleUID {
			out = append(out, "the kept rule "+keptRuleUID+" is in the delete file")
		}
	}
	sort.Strings(got)
	want := append([]string(nil), retiredRuleUIDs...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		out = append(out, "deleteRules uids "+strings.Join(got, ",")+" are not exactly the retired uids "+strings.Join(want, ","))
	}
	live := map[string]bool{}
	for _, g := range a.Groups {
		for _, r := range g.Rules {
			live[r.UID] = true
		}
	}
	for _, u := range retiredRuleUIDs {
		if live[u] {
			out = append(out, "retired rule "+u+" is still defined in the rule groups")
		}
	}
	if !live[keptRuleUID] {
		out = append(out, "the kept rule "+keptRuleUID+" is missing from the rule groups")
	}
	return out
}

const goodDelete = `apiVersion: 1
deleteRules:
  - orgId: 1
    uid: pg-desk-sync-error-retrying
  - orgId: 1
    uid: pg-desk-sync-error-rows
  - orgId: 1
    uid: pg-desk-sync-errors
  - orgId: 1
    uid: pg-desk-unreconciled-anchor-age
`

const goodAlerts = `apiVersion: 1
groups:
  - orgId: 1
    name: g
    rules:
      - uid: pg-desk-sync-stale
`

func TestDeleteFileMatchesRetiredRules(t *testing.T) {
	del, err := os.ReadFile("../../grafana/alerting/alerts-delete.yaml")
	if err != nil {
		t.Fatal(err)
	}
	alerts, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if p := problems(del, alerts); len(p) != 0 {
		t.Fatalf("delete file is wrong: %v", p)
	}
}

func TestDeleteFileCheckRejectsDefects(t *testing.T) {
	if p := problems([]byte(goodDelete), []byte(goodAlerts)); len(p) != 0 {
		t.Fatalf("sound fixture rejected: %v", p)
	}
	cases := []struct {
		name, del, alerts, want string
	}{
		{"dropped uid", strings.Replace(goodDelete, "  - orgId: 1\n    uid: pg-desk-sync-errors\n", "", 1), goodAlerts, "not exactly the retired uids"},
		{"extra uid", goodDelete + "  - orgId: 1\n    uid: pg-desk-liveness-down\n", goodAlerts, "not exactly the retired uids"},
		{"kept rule deleted", goodDelete + "  - orgId: 1\n    uid: pg-desk-sync-stale\n", goodAlerts, "kept rule pg-desk-sync-stale is in the delete file"},
		{"wrong org", strings.Replace(goodDelete, "orgId: 1", "orgId: 2", 1), goodAlerts, "without orgId 1"},
		{"groups key", goodDelete + "groups: []\n", goodAlerts, "groups key"},
		{"retired rule still defined", goodDelete, goodAlerts + "      - uid: pg-desk-sync-error-rows\n", "still defined"},
		{"kept rule absent", goodDelete, "apiVersion: 1\ngroups: []\n", "kept rule pg-desk-sync-stale is missing"},
	}
	for _, c := range cases {
		p := problems([]byte(c.del), []byte(c.alerts))
		if !strings.Contains(strings.Join(p, "\n"), c.want) {
			t.Errorf("%s: want a problem containing %q, got %v", c.name, c.want, p)
		}
	}
}
