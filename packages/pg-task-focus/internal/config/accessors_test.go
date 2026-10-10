package config_test

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
)

// The listing accessors the daemon's GET /config reads: sorted by name, and
// copies the caller may keep.
func TestListingAccessors(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)

	var profiles []string
	for _, p := range cfg.Profiles() {
		profiles = append(profiles, p.Name)
	}
	if want := []string{"normal", "on-call"}; !slices.Equal(profiles, want) {
		t.Errorf("Profiles = %v, want %v", profiles, want)
	}
	var tasks []string
	for _, p := range cfg.Tasks() {
		tasks = append(tasks, p.ID)
	}
	if want := []string{"capacity-check", "end-of-day-summary", "plan-day", "post-plan", "weekly-update"}; !slices.Equal(tasks, want) {
		t.Errorf("Tasks = %v, want %v", tasks, want)
	}
	var cycles []string
	for _, p := range cfg.CycleTypes() {
		cycles = append(cycles, p.ID)
	}
	if want := []string{"deep-work", "notifications", "page-response", "review"}; !slices.Equal(cycles, want) {
		t.Errorf("CycleTypes = %v, want %v", cycles, want)
	}
	if want := []string{"Start of day", "During the day", "End of day"}; !slices.Equal(cfg.GroupOrder(), want) {
		t.Errorf("GroupOrder = %v, want %v", cfg.GroupOrder(), want)
	}
	// The results are copies.
	cfg.GroupOrder()[0] = "changed"
	cfg.Profiles()[0].Daily[0] = "changed"
	if cfg.GroupOrder()[0] != "Start of day" {
		t.Error("GroupOrder shares its slice with the configuration")
	}
	if p, _ := cfg.Profile("normal"); p.Daily[0] != "plan-day" {
		t.Error("Profiles shares its slices with the configuration")
	}
}
