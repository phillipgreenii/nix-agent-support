package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestRegistryInstancesGolden parses testdata/registry-instances.yaml, the
// SAME bytes the Nix module's render test pins (tests/fixtures/
// pg-connector-home/instances.yaml; the check cmp's the two), so the
// Nix-rendered output, the golden and the Go parser are tied together
// (bead pg2-91y12, INV-REG-4).
func TestRegistryInstancesGolden(t *testing.T) {
	reg, err := loadRegistryFile("testdata/registry-instances.yaml")
	if err != nil {
		t.Fatalf("loadRegistryFile: %v", err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	pg2 := []string{"pg-connector-issue-beads", "--beads-dir", "/example/pg2"}
	zr := []string{"pg-connector-issue-beads", "--beads-dir", "/example/zr"}

	issue, err := reg.List("issue")
	if err != nil || !reflect.DeepEqual(issue, []string{"pg-connector-issue-jira", "pg-connector-issue-beads-pg2", "pg-connector-issue-beads-zr"}) {
		t.Fatalf("issue = %v, %v", issue, err)
	}
	if scm, _ := reg.Single("scm"); scm != "pg-connector-scm-git" {
		t.Errorf("scm = %q", scm)
	}
	wantInstances := []string{"pg-connector-issue-beads-pg2", "pg-connector-issue-beads-zr"}
	for name, fn := range map[string]func() ([]string, error){
		"attention": reg.AttentionSources, "search": reg.SearchSources, "activity": reg.ActivitySources,
	} {
		if got, err := fn(); err != nil || !reflect.DeepEqual(got, wantInstances) {
			t.Errorf("%s = %v, %v; want %v", name, got, err, wantInstances)
		}
	}
	if got := reg.Command("pg-connector-issue-beads-pg2"); !reflect.DeepEqual(got, pg2) {
		t.Errorf("pg2 command = %v", got)
	}
	if got := reg.Command("pg-connector-issue-beads-zr"); !reflect.DeepEqual(got, zr) {
		t.Errorf("zr command = %v", got)
	}
	if got := reg.Command("pg-connector-issue-jira"); !reflect.DeepEqual(got, []string{"pg-connector-issue-jira"}) {
		t.Errorf("plain command = %v", got)
	}
	all, err := reg.AllBackends()
	if err != nil || len(all) != 4 { // jira, two beads instances, scm
		t.Errorf("AllBackends = %v, %v; want two instances of one binary as two entries", all, err)
	}
	for _, name := range wantInstances {
		cfg, err := reg.BackendConfig(name)
		if err != nil || !strings.Contains(string(cfg), "Example Person") {
			t.Errorf("BackendConfig(%s) = %s, %v", name, cfg, err)
		}
	}
}
