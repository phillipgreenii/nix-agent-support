package config

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/query"
)

func TestLoad_querySettleDefaultsHoldLimitTo24h(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, queryWrapCfg(`settle = ["/bin/settle", "--x"]`))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	argv, hold, ok := query.SettleOf(c.Queries[0].Query)
	if !ok || len(argv) != 2 || argv[0] != "/bin/settle" || hold != 24*time.Hour {
		t.Fatalf("SettleOf = %v %v %v", argv, hold, ok)
	}
}

func TestLoad_queryAckHoldLimitConfigured(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, queryWrapCfg("settle = [\"/bin/settle\"]\nack_hold_limit = \"6h\""))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, hold, _ := query.SettleOf(c.Queries[0].Query); hold != 6*time.Hour {
		t.Fatalf("hold = %v", hold)
	}
}

func TestLoad_queryAckHoldLimitValidation(t *testing.T) {
	cases := map[string]struct{ extra, want string }{
		"zero":           {"settle = [\"/bin/s\"]\nack_hold_limit = \"0s\"", "ack_hold_limit must be > 0"},
		"negative":       {"settle = [\"/bin/s\"]\nack_hold_limit = \"-1h\"", "ack_hold_limit must be > 0"},
		"without settle": {`ack_hold_limit = "1h"`, "applies only to a query that declares settle"},
		"empty settle":   {`settle = []`, "settle: must not be an empty command"},
		"blank program":  {`settle = [""]`, "must not be empty"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			absentGlobalConfig(t)
			writeCfg(t, queryWrapCfg(tc.extra))
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoad_queryWithoutSettleDeclaresNone(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, queryWrapCfg(""))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, _, ok := query.SettleOf(c.Queries[0].Query); ok {
		t.Fatal("a query without settle must declare none")
	}
}
