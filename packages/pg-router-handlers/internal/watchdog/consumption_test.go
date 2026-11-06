package watchdog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/budget"
	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/usage"
)

// fakeCcpoolOnPath installs a `ccpool` executable that answers only
// `consumption <id> --json` (the process boundary the watchdog reads cost
// across) and returns the argv log path.
func fakeCcpoolOnPath(t *testing.T, json string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\n" +
		"if [ \"$1\" = consumption ]; then printf '%s' '" + json + "'; exit 0; fi\nexit 9\n"
	if err := os.WriteFile(filepath.Join(dir, "ccpool"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// The watchdog's cost ceiling is metered from `ccpool consumption`'s estimated
// cost, not from a price table of its own.
func TestRun_costCeilingTripsOnCcpoolConsumption(t *testing.T) {
	log := fakeCcpoolOnPath(t, `{"model":"m","input_tokens":10,"output_tokens":20,"cache_creation_tokens":0,"cache_read_tokens":0,"estimated_cost_cents":500,"models":[]}`)
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	b := budget.Budget{Cost: 500, Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00}}
	wd := newWD(usage.NewCcpoolReader(ccpool.NewCLIRunner(config.Default())), cc, &recBD{}, b)

	err := wd.Run(context.Background(), "s", "zr-1")

	var be *BudgetError
	if !errors.As(err, &be) || be.Limit != budget.LimitCost || be.Used != 5 || be.Cap != 5 {
		t.Fatalf("err = %v, want a cost BudgetError used=5 cap=5", err)
	}
	argv, _ := os.ReadFile(log)
	if string(argv) != "consumption s --json\n" {
		t.Errorf("ccpool argv log = %q, want the session's external id", argv)
	}
}

func TestRun_belowCostCeilingDoesNotStop(t *testing.T) {
	fakeCcpoolOnPath(t, `{"estimated_cost_cents":100,"models":[]}`)
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	b := budget.Budget{Cost: 500, Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00}}
	wd := newWD(usage.NewCcpoolReader(ccpool.NewCLIRunner(config.Default())), cc, &recBD{}, b)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wd.Run(ctx, "s", "zr-1"); errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("20%% of the cost cap must not hard-stop: %v", err)
	}
}
