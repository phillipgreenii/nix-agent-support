package scriptout

import (
	"context"
	"encoding/json"
	"sort"
)

// Ops returns the op names currently registered in t, sorted for a stable,
// deterministic capabilities.ops response. It is literally t's own keys —
// there is no second, separately-maintained list this can drift from: an
// op present in the table is reported, an op absent from the table is not,
// with nothing in between for a backend author to get wrong.
func (t DispatchTable) Ops() []string {
	ops := make([]string, 0, len(t))
	for op := range t {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops
}

// AddCapabilities inserts the capabilities op's handler into table and
// returns table (for chaining straight out of a newDispatchTable-style
// builder). The handler answers with resp, except its Ops field: that is
// computed fresh, on every call, as table.Ops() (this very entry included,
// since it is inserted into table before Handle ever runs) — never from
// resp.Ops.
//
// resp.Ops MUST be left unset (nil/empty). AddCapabilities panics at
// startup if it isn't, because populating it is the exact mistake this
// helper exists to make impossible: hand-typing a second, separately
// maintained copy of the ops list that can silently drift from what the
// dispatch table actually answers — either overclaiming an op the table
// doesn't actually have wired, or omitting one it does (bead pg2-fh2vh).
// That panic is what turns a would-be silent gap into a startup-time
// error: a backend binary that tries to reintroduce a literal Ops list
// crashes the instant its dispatch table is built, before ServeLoop ever
// reads a request.
//
// schemaVersion is the OpHandler.SchemaVersion this capabilities entry
// itself carries (ServeLoop's error-path schemaVersion stamp) — pass the
// capability's own schema version, the same number resp's own
// SchemaVersions map entry already carries.
func AddCapabilities(table DispatchTable, schemaVersion int, resp CapabilitiesResponse) DispatchTable {
	if len(resp.Ops) != 0 {
		panic("scriptout: AddCapabilities: resp.Ops must be left unset — Ops is computed from the dispatch table's own registered op names, never passed in")
	}
	table[OpCapabilities] = OpHandler{
		SchemaVersion: schemaVersion,
		Handle: func(_ context.Context, _ json.RawMessage) (any, error) {
			out := resp
			out.Ops = table.Ops()
			return out, nil
		},
	}
	return table
}
