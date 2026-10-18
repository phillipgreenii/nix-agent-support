package servetest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// This file holds the readers and the client runner the change engine's
// real-process tests use: the change log, the pending settle rows, an entity's
// version and a group's stored state, each read straight from the daemon's store
// file (so a test that must say "no change was logged" asserts on the table, not
// on the absence of a client answer), and the real client binary.

// ChangeLogRow is one row of the change_log table. ID is the wire reference
// owner/repo#n (the log's key; the entity key is the PR's node id).
type ChangeLogRow struct {
	Seq     int64
	Kind    string
	ID      string
	Version int64
	HeadSHA string
	Kinds   []string
	Origin  string
}

// PendingRow is one row of the pending_change table. ID is the entity key (the
// PR's node id).
type PendingRow struct {
	Kind, ID string
	Kinds    []string
	// Awaited names the groups the row still waits for.
	Awaited  []string
	HeadSHA  string
	Origin   string
	Deadline time.Time
}

// GroupState is the stored state of one entity group.
type GroupState struct {
	// Present is false when the store has no row for the group.
	Present    bool
	FreshUntil time.Time
	LastError  string
	Content    string
	Generation int64
}

// readDB opens the store file read-only for one read and closes it again, so a
// read after a crash (a hot WAL) never meets a handle opened before it.
func (d *Daemon) readDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(d.StateDir, "store.sqlite")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func millis(ms sql.NullInt64) time.Time {
	if !ms.Valid {
		return time.Time{}
	}
	return time.UnixMilli(ms.Int64)
}

func decodeKinds(t *testing.T, raw string) []string {
	t.Helper()
	var out []string
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("servetest: kinds %q: %v", raw, err)
	}
	return out
}

// ChangeLog returns every row of the change log, oldest first.
func (d *Daemon) ChangeLog(t *testing.T) []ChangeLogRow {
	t.Helper()
	db := d.readDB(t)
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT seq, kind, id, version, head_sha, kinds, origin FROM change_log ORDER BY seq`)
	if err != nil {
		t.Fatalf("servetest: read the change log: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ChangeLogRow
	for rows.Next() {
		var r ChangeLogRow
		var kinds string
		if err := rows.Scan(&r.Seq, &r.Kind, &r.ID, &r.Version, &r.HeadSHA, &kinds, &r.Origin); err != nil {
			t.Fatal(err)
		}
		r.Kinds = decodeKinds(t, kinds)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// PendingRows returns every pending settle row.
func (d *Daemon) PendingRows(t *testing.T) []PendingRow {
	t.Helper()
	db := d.readDB(t)
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT kind, id, kinds, awaited, head_sha, origin, deadline FROM pending_change ORDER BY kind, id`)
	if err != nil {
		t.Fatalf("servetest: read the pending rows: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PendingRow
	for rows.Next() {
		var r PendingRow
		var kinds, awaited string
		var deadline sql.NullInt64
		if err := rows.Scan(&r.Kind, &r.ID, &kinds, &awaited, &r.HeadSHA, &r.Origin, &deadline); err != nil {
			t.Fatal(err)
		}
		r.Kinds = decodeKinds(t, kinds)
		r.Deadline = millis(deadline)
		if awaited != "" {
			var aw []struct {
				Group string `json:"group"`
			}
			if err := json.Unmarshal([]byte(awaited), &aw); err != nil {
				t.Fatalf("servetest: awaited %q: %v", awaited, err)
			}
			for _, a := range aw {
				r.Awaited = append(r.Awaited, a.Group)
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// EntityVersion is the version of a stored entity (0 when it has no row).
func (d *Daemon) EntityVersion(t *testing.T, kind, id string) int64 {
	t.Helper()
	db := d.readDB(t)
	defer func() { _ = db.Close() }()
	var v int64
	err := db.QueryRow(`SELECT version FROM entity WHERE kind = ? AND id = ?`, kind, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatalf("servetest: read the version of %s/%s: %v", kind, id, err)
	}
	return v
}

// Group reads the stored state of one group of a pull request (kind "pr").
func (d *Daemon) Group(t *testing.T, nodeID, group string) GroupState {
	t.Helper()
	db := d.readDB(t)
	defer func() { _ = db.Close() }()
	var g GroupState
	var fresh sql.NullInt64
	var content []byte
	err := db.QueryRow(`SELECT fresh_until, last_error, content, generation FROM entity_group WHERE kind = 'pr' AND id = ? AND group_name = ?`,
		nodeID, group).Scan(&fresh, &g.LastError, &content, &g.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return GroupState{}
	}
	if err != nil {
		t.Fatalf("servetest: read the group %s of %s: %v", group, nodeID, err)
	}
	g.Present = true
	g.FreshUntil = millis(fresh)
	g.Content = string(content)
	return g
}

// Stale reports whether the group is stale as the store shows it: absent, past
// its freshness, or carrying the error of its last fetch.
func (g GroupState) Stale(now time.Time) bool {
	return !g.Present || g.LastError != "" || !g.FreshUntil.After(now)
}

// RunClient runs the real client binary (this daemon's binary in its pr or ci
// mode) once, on an empty environment with the request on standard input, and
// returns its answer. A run answers exactly one request, and the client never
// sends changes_ack by itself: a test that acknowledges sends a second run.
func (d *Daemon) RunClient(t *testing.T, instance string, req scriptout.Request) scriptout.Response {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(d.bin, "--config", d.ConfigPath, instance)
	cmd.Dir = "/"
	cmd.Env = []string{}
	cmd.Stdin = bytes.NewReader(raw)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("servetest: the client binary (%s): %v; stdout %q stderr %q", instance, err, out.String(), strings.TrimSpace(errb.String()))
	}
	var resp scriptout.Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("servetest: the client binary answered %q: %v", out.String(), err)
	}
	return resp
}
