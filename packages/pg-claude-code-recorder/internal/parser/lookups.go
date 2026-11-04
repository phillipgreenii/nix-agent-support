package parser

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// The lookup columns of records, in the order insert binds them. A field whose
// storage is `record:<column>` with one of these columns is interned into the
// column's lookup table (id, v) and the id is written on the record. Any other
// record column is not routed: that field stays a fields row.
const (
	lkCwd = iota
	lkBranch
	lkVersion
	lkEntrypoint
	lkUserType
	lkCount
)

var lookupDefs = [lkCount]struct{ col, table string }{
	lkCwd:        {"cwd_id", "lk_cwd"},
	lkBranch:     {"branch_id", "lk_branch"},
	lkVersion:    {"version_id", "lk_version"},
	lkEntrypoint: {"entrypoint_id", "lk_entrypoint"},
	lkUserType:   {"user_type_id", "lk_user_type"},
}

// lookupRoutes maps, per source name, a field name to its lookup slot, from the
// store tags the source declares.
func lookupRoutes(srcs []source.Source) map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, s := range srcs {
		st, ok := s.(source.Stored)
		if !ok {
			continue
		}
		for name, d := range st.Storage() {
			if d.Kind != source.StoreRecord {
				continue
			}
			for slot, def := range lookupDefs {
				if def.col == d.Column {
					if out[s.Name()] == nil {
						out[s.Name()] = map[string]int{}
					}
					out[s.Name()][name] = slot
				}
			}
		}
	}
	return out
}

// lookupID returns the id of value v in the slot's lookup table, creating the
// row on first sight. The cache lives for the parser's lifetime and is valid
// only while lkEpoch equals Store.RollbackEpoch, the same rule as the
// field-name cache: a rolled-back write may have undone a lookup row and its id
// may be reused. Called inside a write transaction.
func (p *Parser) lookupID(tx *pstx, slot int, v string) (int64, error) {
	if ep := p.cfg.Store.RollbackEpoch(); p.lkEpoch != ep || p.lk[slot] == nil {
		if p.lkEpoch != ep {
			p.lk = [lkCount]map[string]int64{}
		}
		p.lkEpoch = ep
		if p.lk[slot] == nil {
			p.lk[slot] = map[string]int64{}
		}
	}
	if id, ok := p.lk[slot][v]; ok {
		return id, nil
	}
	tbl := lookupDefs[slot].table
	if _, err := tx.Exec(`INSERT INTO `+tbl+`(v) VALUES(?) ON CONFLICT(v) DO NOTHING`, v); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM `+tbl+` WHERE v=?`, v).Scan(&id); err != nil {
		return 0, err
	}
	p.lk[slot][v] = id
	return id, nil
}

// recordLookups resolves the lookup ids of one record: the value of each
// routed field that is a non-empty string (the string kinds convert
// losslessly by construction, so no mismatch arises here). A field
// repeated in one record keeps its last value, like its fields row. An id is
// nil when the record does not carry the value.
func (p *Parser) recordLookups(tx *pstx, srcName string, r source.Record) ([lkCount]any, error) {
	var ids [lkCount]any
	route := p.routes[srcName]
	if route == nil {
		return ids, nil
	}
	var vals [lkCount]string
	var have [lkCount]bool
	for _, fl := range r.Fields {
		slot, ok := route[fl.Name]
		if !ok || fl.S == "" {
			continue
		}
		vals[slot], have[slot] = fl.S, true
	}
	for slot := range ids {
		if !have[slot] {
			continue
		}
		id, err := p.lookupID(tx, slot, vals[slot])
		if err != nil {
			return ids, err
		}
		ids[slot] = id
	}
	return ids, nil
}
