package parser

import (
	"bytes"
	"database/sql"
	"encoding/json"
)

// ParseTail parses the bytes after the last newline of a generation's copy as a
// complete final line when they are valid JSON (the settle check's torn-tail
// rule: a crash left the line without its newline). It advances the parse
// offset to the copy offset and reports true; any other tail is left alone and
// the answer is false.
func (p *Parser) ParseTail(file int64, gen int) (bool, error) {
	p.run.Lock()
	defer p.run.Unlock()
	f, err := p.cfg.Store.File(file)
	if err != nil {
		return false, err
	}
	src, ok := p.bySrc[f.Source]
	if !ok || f.Mode != "append" {
		return false, nil
	}
	g, err := p.cfg.Store.Generation(file, gen)
	if err != nil {
		return false, err
	}
	if g.ParseOffset >= g.CopyOffset || g.CopyOffset-g.ParseOffset > int64(p.cfg.MaxLineBytes) {
		return false, nil
	}
	rel, ok := p.relPath(f.ID)
	if !ok {
		return false, nil
	}
	caps, ok := src.Match(rel)
	if !ok {
		return false, nil
	}
	fh, err := p.cfg.Store.OpenPointer(f.ID, g.Gen)
	if err != nil {
		return false, err
	}
	defer func() { _ = fh.Close() }()
	buf := make([]byte, g.CopyOffset-g.ParseOffset)
	if _, err := fh.ReadAt(buf, g.ParseOffset); err != nil {
		return false, err
	}
	line := bytes.TrimSpace(buf)
	if bytes.IndexByte(buf, '\n') >= 0 || !json.Valid(line) {
		return false, nil
	}
	recs, err := src.Parse(caps, line)
	if err != nil {
		return false, nil
	}
	err = p.cfg.Store.WithWrite("parse.before-commit", func(rawTx *sql.Tx) error {
		tx := newPS(rawTx)
		defer tx.Close()
		if err := p.genReset(tx, f, src, g, g.ParseOffset); err != nil {
			return err
		}
		if err := p.insert(tx, f, src, caps, g, g.ParseOffset, len(buf), recs); err != nil {
			return err
		}
		res, err := tx.Exec(`UPDATE generations SET parse_offset=? WHERE file_id=? AND gen=? AND ?<=copy_offset`, g.CopyOffset, f.ID, g.Gen, g.CopyOffset)
		if err != nil {
			return err
		}
		p.noteParseOffset(res, f.ID, g.Gen)
		if _, err := tx.Exec(`INSERT INTO parse_state(file_id,gen,version) VALUES(?,?,?) ON CONFLICT(file_id,gen) DO UPDATE SET version=excluded.version`,
			f.ID, g.Gen, src.Version()); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('parse_seq','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`); err != nil {
			return err
		}
		if err := p.flushSessions(tx); err != nil {
			return err
		}
		return p.mark(tx)
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
