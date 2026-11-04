package parser

import "sort"

// sessionBatch is what one write transaction learned about sessions: per
// session the newest record time, the records written and the files that hold
// them. flushSessions writes it as one upsert per session (not per record) just
// before the transaction commits; a transaction that rolls back drops it.
type sessionBatch struct {
	by map[int64]*sessionDelta
}

type sessionDelta struct {
	last  int64
	count int64
	files map[int64]struct{}
	// first is the first lookup id seen in this transaction for the version,
	// entrypoint and user type slots (nil: none seen).
	first [3]any
}

// sessionRef returns the integer reference of a session, creating its sessions
// row on first sight (first-seen order; the empty session gets a sentinel row
// with session_id ”). The cache survives across transactions and is valid only
// while sessEpoch equals Store.RollbackEpoch, the same rule as the field-name
// cache: a rolled-back write may have undone a sessions row. Called inside a
// write transaction.
func (p *Parser) sessionRef(tx *pstx, id, project string, firstNS int64) (int64, error) {
	if ep := p.cfg.Store.RollbackEpoch(); p.sessRefs == nil || p.sessEpoch != ep {
		m := map[string]int64{}
		rows, err := tx.Query(`SELECT session_id, session_ref FROM sessions`)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var sid string
			var ref int64
			if err := rows.Scan(&sid, &ref); err != nil {
				_ = rows.Close()
				return 0, err
			}
			m[sid] = ref
		}
		if err := rows.Close(); err != nil {
			return 0, err
		}
		if err := rows.Err(); err != nil {
			return 0, err
		}
		p.sessRefs, p.sessEpoch = m, ep
	}
	if ref, ok := p.sessRefs[id]; ok {
		return ref, nil
	}
	var proj any
	if project != "" {
		proj = project
	}
	if _, err := tx.Exec(`INSERT INTO sessions(session_id,project,first_time_ns) VALUES(?,?,?) ON CONFLICT(session_id) DO NOTHING`, id, proj, firstNS); err != nil {
		return 0, err
	}
	var ref int64
	if err := tx.QueryRow(`SELECT session_ref FROM sessions WHERE session_id=?`, id).Scan(&ref); err != nil {
		return 0, err
	}
	p.sessRefs[id] = ref
	return ref, nil
}

// noteSession records one record of session ref at time tns in file.
func (tx *pstx) noteSession(ref, tns, file int64, lk [lkCount]any) {
	if tx.sess == nil {
		tx.sess = &sessionBatch{by: map[int64]*sessionDelta{}}
	}
	d := tx.sess.by[ref]
	if d == nil {
		d = &sessionDelta{last: tns, files: map[int64]struct{}{}}
		tx.sess.by[ref] = d
	}
	if tns > d.last {
		d.last = tns
	}
	d.count++
	d.files[file] = struct{}{}
	for i, slot := range [3]int{lkVersion, lkEntrypoint, lkUserType} {
		if d.first[i] == nil {
			d.first[i] = lk[slot]
		}
	}
}

// flushSessions upserts session_stats and inserts session_files for what the
// transaction wrote, in session order so two runs write the same sequence.
func (p *Parser) flushSessions(tx *pstx) error {
	if tx.sess == nil {
		return nil
	}
	refs := make([]int64, 0, len(tx.sess.by))
	for ref := range tx.sess.by {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i] < refs[j] })
	for _, ref := range refs {
		d := tx.sess.by[ref]
		if _, err := tx.Exec(`INSERT INTO session_stats(session_ref,last_time_ns,record_count) VALUES(?,?,?)
ON CONFLICT(session_ref) DO UPDATE SET last_time_ns=MAX(last_time_ns,excluded.last_time_ns), record_count=record_count+excluded.record_count`,
			ref, d.last, d.count); err != nil {
			return err
		}
		// The session's first version, entrypoint and user type: the first record
		// that carries one fills it; a later one never changes it.
		if d.first[0] != nil || d.first[1] != nil || d.first[2] != nil {
			if _, err := tx.Exec(`UPDATE sessions SET first_version_id=COALESCE(first_version_id,?1), entrypoint_id=COALESCE(entrypoint_id,?2), user_type_id=COALESCE(user_type_id,?3) WHERE session_ref=?4`,
				d.first[0], d.first[1], d.first[2], ref); err != nil {
				return err
			}
		}
		files := make([]int64, 0, len(d.files))
		for f := range d.files {
			files = append(files, f)
		}
		sort.Slice(files, func(i, j int) bool { return files[i] < files[j] })
		for _, f := range files {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO session_files(session_ref,file_id) VALUES(?,?)`, ref, f); err != nil {
				return err
			}
		}
	}
	tx.sess = nil
	return nil
}

// recomputeSessionStats rebuilds session_stats for the named sessions from
// records, after rows were deleted. sessions and session_files rows are shared
// and stay.
func (p *Parser) recomputeSessionStats(tx *pstx, ids []string) error {
	q := `UPDATE session_stats SET
  last_time_ns=COALESCE((SELECT MAX(time_ns) FROM records WHERE session=?1),0),
  record_count=(SELECT COUNT(*) FROM records WHERE session=?1)
WHERE session_ref=(SELECT session_ref FROM sessions WHERE session_id=?1)`
	if !p.cfg.Store.HasSessionText() {
		// The version 3 layout has no session text: the id names its sessions row.
		q = `UPDATE session_stats SET
  last_time_ns=COALESCE((SELECT MAX(time_ns) FROM records WHERE session_ref=session_stats.session_ref),0),
  record_count=(SELECT COUNT(*) FROM records WHERE session_ref=session_stats.session_ref)
WHERE session_ref=(SELECT session_ref FROM sessions WHERE session_id=?1)`
	}
	for _, id := range ids {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

// sessionsOfGen lists the sessions that have records in one file generation.
func (p *Parser) sessionsOfGen(tx *pstx, file int64, gen int) ([]string, error) {
	q := `SELECT DISTINCT session FROM records WHERE file_id=? AND gen=?`
	if !p.cfg.Store.HasSessionText() {
		q = `SELECT DISTINCT s.session_id FROM records r JOIN sessions s ON s.session_ref=r.session_ref WHERE r.file_id=? AND r.gen=?`
	}
	rows, err := tx.Query(q, file, gen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
