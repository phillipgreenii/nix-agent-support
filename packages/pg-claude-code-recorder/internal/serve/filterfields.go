package serve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// noteFilterFields records which field names a request's filter used: one
// counter per (op, field) and one info line. Names come from the catalog, so
// the label set is bounded; query text and values are never logged.
func (d *Daemon) noteFilterFields(op string, fields map[string]int) {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		d.obs.Reg.Add(obs.MFilterFields, map[string]string{"op": op, "field": n}, float64(fields[n]))
		parts[i] = fmt.Sprintf("%s=%d", n, fields[n])
	}
	d.obs.Log.Info("query_filter_fields", "op", op, "fields", strings.Join(parts, ","))
}
