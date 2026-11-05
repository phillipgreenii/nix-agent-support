package parser

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// extRoute is where a source field goes in an index.db that has the extension
// tables: column col of table (an index into store.ExtTables).
type extRoute struct {
	table int
	col   string
}

// extRoutes maps source name, then field name, to its route. A declaration
// that names no registered table or column, or a column whose type cannot hold
// the field's kind, is left out (the field stays in fields) and is listed by
// CheckExtDeclarations.
func extRoutes(srcs []source.Source) (map[string]map[string]extRoute, []string) {
	reg := store.ExtTables()
	out := map[string]map[string]extRoute{}
	var problems []string
	for _, s := range srcs {
		sd, ok := s.(source.Stored)
		if !ok {
			continue
		}
		var kinds map[string]source.Kind
		if c, ok := s.(source.Catalogued); ok {
			kinds = c.Catalog()
		}
		for name, st := range sd.Storage() {
			if st.Kind != source.StoreExt {
				continue
			}
			ti := -1
			for i, t := range reg {
				if t.Name == st.Table {
					ti = i
				}
			}
			if ti < 0 {
				problems = append(problems, fmt.Sprintf("%s: %s declares %s, an unregistered extension table", s.Name(), name, st))
				continue
			}
			col, ok := reg[ti].Column(st.Column)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: %s declares %s, which has no such column", s.Name(), name, st))
				continue
			}
			if !extTypeFits(col.Type, kinds[name]) {
				problems = append(problems, fmt.Sprintf("%s: %s is a %s field but %s is %s", s.Name(), name, kinds[name], st, col.Type))
				continue
			}
			if out[s.Name()] == nil {
				out[s.Name()] = map[string]extRoute{}
			}
			out[s.Name()][name] = extRoute{table: ti, col: st.Column}
		}
	}
	return out, problems
}

// extTypeFits reports whether a STRICT column type holds a field of the kind.
func extTypeFits(colType string, k source.Kind) bool {
	switch k {
	case source.KindID, source.KindKeyword, source.KindPath, source.KindSession:
		return colType == "TEXT"
	case source.KindFloat:
		return colType == "REAL"
	case source.KindInt, source.KindBool, source.KindTime:
		return colType == "INTEGER"
	}
	return false
}

// CheckExtDeclarations reports the extension-table declarations of srcs that
// do not resolve to a registered column of a compatible type, nil when all do.
func CheckExtDeclarations(srcs []source.Source) error {
	if _, problems := extRoutes(srcs); len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// extRow collects the values one record routes into one extension table.
type extRow struct {
	vals map[string]any
}

// insertExt writes one row per extension table that received a value, keyed by
// the record id; a repeated column keeps the last value (as fields does).
func insertExt(tx *pstx, id int64, rows map[int]*extRow) error {
	reg := store.ExtTables()
	for ti := range reg {
		r := rows[ti]
		if r == nil {
			continue
		}
		cols, marks, args := []string{"rec_id"}, []string{"?"}, []any{id}
		for _, c := range reg[ti].Columns {
			if v, ok := r.vals[c.Name]; ok {
				cols, marks, args = append(cols, c.Name), append(marks, "?"), append(args, v)
			}
		}
		q := "INSERT OR REPLACE INTO " + reg[ti].Table() + "(" + strings.Join(cols, ",") + ") VALUES(" + strings.Join(marks, ",") + ")"
		if _, err := tx.ExecL(tblExt, q, args...); err != nil {
			return err
		}
	}
	return nil
}

// clearExt deletes the extension rows of a file generation's records. It runs
// before the records are deleted.
func (p *Parser) clearExt(tx *pstx, file int64, gen int) error {
	if !p.cfg.Store.HasExt() {
		return nil
	}
	for _, t := range store.ExtTables() {
		if _, err := tx.Exec(`DELETE FROM `+t.Table()+` WHERE rec_id IN (SELECT id FROM records WHERE file_id=?1 AND gen=?2)`, file, gen); err != nil {
			return err
		}
	}
	return nil
}
