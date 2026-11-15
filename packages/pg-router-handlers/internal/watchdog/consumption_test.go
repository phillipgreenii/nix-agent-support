package watchdog

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/budget"
	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/usage"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// fakeCcpool is a ccpool CLIRunner whose process boundary is a cmdruntest.Fake
// that answers only `consumption <id> --json` (what the watchdog reads cost
// across) and fails every other subcommand with exit 9. It returns the runner
// and the fake, whose Calls are the argv log.
func fakeCcpool(t *testing.T, json string) (*ccpool.CLIRunner, *cmdruntest.Fake) {
	t.Helper()
	f := cmdruntest.New(t)
	f.On(func(c cmdrun.Call) bool { return c.Name == "ccpool" && len(c.Args) > 0 && c.Args[0] == "consumption" }).Return(cmdruntest.Out(json))
	f.On(cmdruntest.MatchName("ccpool")).Return(cmdruntest.Fail(9, ""))
	cli := ccpool.NewCLIRunner(config.Default())
	cli.Exec = f.Runner()
	return cli, f
}

// The watchdog's cost ceiling is metered from `ccpool consumption`'s estimated
// cost, not from a price table of its own.
func TestRun_costCeilingTripsOnCcpoolConsumption(t *testing.T) {
	cli, fk := fakeCcpool(t, `{"model":"m","input_tokens":10,"output_tokens":20,"cache_creation_tokens":0,"cache_read_tokens":0,"estimated_cost_cents":500,"models":[]}`)
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	b := budget.Budget{Cost: 500, Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00}}
	wd := newWD(usage.NewCcpoolReader(cli), cc, &recBD{}, b)

	err := wd.Run(context.Background(), "s", "zr-1")

	var be *BudgetError
	if !errors.As(err, &be) || be.Limit != budget.LimitCost || be.Used != 5 || be.Cap != 5 {
		t.Fatalf("err = %v, want a cost BudgetError used=5 cap=5", err)
	}
	calls := fk.Calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Args, []string{"consumption", "s", "--json"}) {
		t.Errorf("ccpool calls = %v, want one `consumption s --json` (the session's external id)", calls)
	}
}

func TestRun_belowCostCeilingDoesNotStop(t *testing.T) {
	cli, _ := fakeCcpool(t, `{"estimated_cost_cents":100,"models":[]}`)
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	b := budget.Budget{Cost: 500, Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00}}
	wd := newWD(usage.NewCcpoolReader(cli), cc, &recBD{}, b)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wd.Run(ctx, "s", "zr-1"); errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("20%% of the cost cap must not hard-stop: %v", err)
	}
}
