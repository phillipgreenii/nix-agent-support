package corpus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// Facts is where the Monitor reads Claude Code's files from: the recorder.
// recorderfacts.Facts satisfies it. Client returns the connected client (or an
// error matching session.ErrDaemonOffline while the recorder is down) and Wrap
// turns a transport failure of a call made on that client into the same error.
type Facts interface {
	Client(ctx context.Context) (*client.Client, error)
	Wrap(err error) error
}

const (
	sourceTranscript = "claude-transcript"
	sourceStatus     = "claude-status"

	// linePage is how many line records one raw read asks for.
	linePage = 500
	// feedPage is how many feed entries one poll asks for.
	feedPage = 1000
	// readTimeout bounds one Scan's reads of the recorder.
	readTimeout = 20 * time.Second
)

type fileKind int

const (
	kindTranscript fileKind = iota
	kindSubagent
	kindStatus
)

// fileState is what pa-monitor keeps per recorder file: the per-file offset
// cursor (next is the offset of the last line folded) and the fold of the lines
// read so far.
type fileState struct {
	ref     string // the record id's file id, "f_N"
	id      int64
	kind    fileKind
	project string
	session string // the file's path session (the main transcript's id)
	agent   string

	next  int64 // offset of the last line folded; -1 before the first
	last  time.Time
	title string
	dirty bool

	acc     *transcript.Accumulator
	snap    transcript.Snapshot
	records []usage.Record
	status  []limits.Record
}

func newFileState(id int64, kind fileKind, project, session, agent string) *fileState {
	fs := &fileState{
		ref: fmt.Sprintf("f_%d", id), id: id, kind: kind,
		project: project, session: session, agent: agent,
		next: -1, dirty: true,
	}
	if kind != kindStatus {
		fs.acc = transcript.NewAccumulator()
	}
	return fs
}

// reset drops everything folded from the file (a reset entry of the feed says
// its bytes were replaced) and marks it for a re-read from the start.
func (f *fileState) reset() {
	*f = *newFileState(f.id, f.kind, f.project, f.session, f.agent)
}

var customTitleMarker = []byte("custom-title")

// fold folds one line (without its newline) read at offset.
func (f *fileState) fold(line []byte, offset int64, at time.Time) {
	f.next = offset
	if at.After(f.last) {
		f.last = at
	}
	if f.kind == kindStatus {
		f.status = append(f.status, limits.ParseStatusRecords(line)...)
		return
	}
	f.acc.FoldLine(line)
	if bytes.Contains(line, customTitleMarker) {
		var rec struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Type == "custom-title" {
			f.title = rec.CustomTitle
		}
	}
}

// finish refreshes the derived values after a batch of lines.
func (f *fileState) finish() {
	f.dirty = false
	if f.kind == kindStatus {
		return
	}
	f.snap = f.acc.Snapshot()
	f.records = f.acc.Records()
}

// reader keeps the files pa-monitor folds and the feed cursor that says which
// of them changed. Nothing here opens a file of the Claude directory: lines
// come from the recorder's raw bytes.
type reader struct {
	facts   Facts
	cursor  string
	pending []client.InventoryRequest // inventory requests still to run after a bootstrap
	gate    func() error              // test seam: called before each inventory request
	files   map[string]*fileState     // by file ref
	asked   map[string]bool           // session ids whose files were asked for by id

	// perf counters of the last sync.
	queries     int
	linesFolded int
	filesFolded int
}

func newReader(f Facts) *reader {
	return &reader{facts: f, files: map[string]*fileState{}, asked: map[string]bool{}}
}

func eqStr(field, v string) client.Filter {
	b, _ := json.Marshal(v)
	return client.Filter{Field: field, Eq: b}
}

func str(v any) string { s, _ := v.(string); return s }

func parseTime(v any) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, str(v))
	return t
}

func fileID(ref string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimPrefix(ref, "f_"), 10, 64)
	return n, err == nil && strings.HasPrefix(ref, "f_")
}

func kindOf(source, agent string) fileKind {
	switch {
	case source == sourceStatus:
		return kindStatus
	case agent != "":
		return kindSubagent
	}
	return kindTranscript
}

func sourcesFilter() client.Filter {
	return client.Filter{Any: []client.Filter{eqStr("source", sourceTranscript), eqStr("source", sourceStatus)}}
}

// inventoryPage is how many files one inventory request asks for.
const inventoryPage = 10000

// bootstrap takes the feed cursor first (so nothing committed after it is
// missed) and plans the inventory of the files that matter: transcripts with
// records since cutoff or belonging to an active session, and every status
// file. The cursor does not depend on the inventory: it is kept when the
// inventory fails or times out, and resumeInventory runs the steps still
// pending on the next Scan.
func (r *reader) bootstrap(ctx context.Context, c *client.Client, cutoff time.Time, active []string) error {
	head, err := r.query(ctx, c, client.QueryRequest{From: "records", Select: []string{"type"}, Limit: 1})
	if err != nil {
		return err
	}
	r.files = map[string]*fileState{}
	r.asked = map[string]bool{}
	r.cursor = head.AsOf
	r.pending = planInventory(cutoff, active)
	return nil
}

// planInventory lists the inventory requests: the status files, then the
// transcripts with records since cutoff plus the active sessions' transcripts.
// The recorder answers each from its per-file table, so each is one cheap call.
func planInventory(cutoff time.Time, active []string) []client.InventoryRequest {
	return []client.InventoryRequest{
		{Sources: []string{sourceStatus}},
		{Sources: []string{sourceTranscript}, Since: cutoff.UTC().Format(time.RFC3339), Sessions: active},
	}
}

// resumeInventory runs the planned inventory requests that are not done yet. A
// request that fails stays pending, with every request before it kept. A request
// the recorder answers degraded (its per-file table is still being backfilled, or
// it is behind) may have left files out, so it also stays pending, and it and the
// requests after it run again on the next Scan. The files listed so far are kept
// and the Scan goes on with them, so a recorder that is catching up neither
// stalls the reading of the status and transcript files nor makes a partial
// (possibly empty) list final.
func (r *reader) resumeInventory(ctx context.Context, c *client.Client) error {
	for len(r.pending) > 0 {
		if r.gate != nil {
			if err := r.gate(); err != nil {
				return err
			}
		}
		degraded, err := r.inventory(ctx, c, r.pending[0])
		if err != nil {
			return err
		}
		if degraded {
			return nil
		}
		r.pending = r.pending[1:]
	}
	return nil
}

// inventory lists the files that match req (all its pages) and adds the ones
// not known yet. It reports whether any page was answered degraded (files may
// be missing from the list).
func (r *reader) inventory(ctx context.Context, c *client.Client, req client.InventoryRequest) (degraded bool, err error) {
	req.Limit = inventoryPage
	for {
		r.queries++
		ans, err := c.FilesInventory(ctx, req)
		if err != nil {
			return degraded, err
		}
		degraded = degraded || ans.Degraded
		for _, f := range ans.Files {
			ref := fmt.Sprintf("f_%d", f.File)
			last, _ := time.Parse(time.RFC3339Nano, f.Last)
			if fs, ok := r.files[ref]; ok {
				if last.After(fs.last) {
					fs.last = last
				}
				continue
			}
			fs := newFileState(f.File, kindOf(f.Source, f.AgentID), f.Project, f.FileSession, f.AgentID)
			fs.last = last
			r.files[ref] = fs
		}
		if !ans.More || len(ans.Files) == 0 {
			return degraded, nil
		}
		req.After = ans.Files[len(ans.Files)-1].File
	}
}

// ensureSessions asks, once per session, for the files of the active sessions
// the inventory did not cover.
func (r *reader) ensureSessions(ctx context.Context, c *client.Client, ids []string) error {
	var missing []string
	have := map[string]bool{}
	for _, f := range r.files {
		have[f.session] = true
	}
	for _, id := range ids {
		if !have[id] && !r.asked[id] {
			missing = append(missing, id)
			r.asked[id] = true
		}
	}
	if len(missing) == 0 {
		return nil
	}
	degraded, err := r.inventory(ctx, c, client.InventoryRequest{Sources: []string{sourceTranscript}, Sessions: missing})
	if err != nil || degraded {
		// Ask again on the next Scan: the answer failed or may have left files out.
		for _, id := range missing {
			delete(r.asked, id)
		}
	}
	return err
}

// drain applies what the feed says changed since the cursor: a reset entry
// drops the file's folded lines BEFORE anything else is applied to it, and a
// record entry marks its file for a read of the lines after its offset cursor.
// A record already folded is skipped by that cursor, so a repeat (delivery is at
// least once) changes nothing.
func (r *reader) drain(ctx context.Context, c *client.Client) error {
	where := sourcesFilter()
	for {
		r.queries++
		ans, err := c.FeedPage(ctx, &where, r.cursor, []string{"source", "project", "file_session", "agent_id", "time"}, feedPage, 0)
		if err != nil {
			return err
		}
		for _, en := range ans.Entries {
			switch en.Kind {
			case "reset":
				if fs := r.files[en.File]; fs != nil {
					fs.reset()
				}
			case "record":
				if en.Record == nil {
					continue
				}
				fs := r.files[en.Record.File]
				if fs == nil {
					id, ok := fileID(en.Record.File)
					if !ok {
						continue
					}
					fs = newFileState(id, kindOf(str(en.Fields["source"]), str(en.Fields["agent_id"])),
						str(en.Fields["project"]), str(en.Fields["file_session"]), str(en.Fields["agent_id"]))
					r.files[fs.ref] = fs
				}
				if en.Record.Offset > fs.next {
					fs.dirty = true
				}
			}
		}
		r.cursor = ans.NextCursor
		if !ans.More {
			return nil
		}
	}
}

func (r *reader) query(ctx context.Context, c *client.Client, req client.QueryRequest) (*client.QueryAnswer, error) {
	r.queries++
	return c.Query(ctx, req)
}

// foldDirty reads the lines after each dirty file's offset cursor from the
// recorder (raw bytes, in offset order) and folds them.
func (r *reader) foldDirty(ctx context.Context, c *client.Client) (firstErr error) {
	for _, fs := range r.files {
		if !fs.dirty {
			continue
		}
		n, err := r.readLines(ctx, c, fs)
		if n > 0 {
			r.filesFolded++
		}
		if err != nil {
			if errors.Is(err, client.ErrDown) || errors.Is(err, client.ErrUnavailable) {
				return err
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		fs.finish()
	}
	return firstErr
}

func (r *reader) readLines(ctx context.Context, c *client.Client, fs *fileState) (int, error) {
	total := 0
	for {
		ans, err := r.query(ctx, c, client.QueryRequest{
			From: "records",
			Where: &client.Filter{All: []client.Filter{
				{Field: "file", Eq: json.RawMessage(strconv.FormatInt(fs.id, 10))},
				{Field: "item", Eq: json.RawMessage("-1")},
				{Field: "offset", Range: &client.Range{Gt: fs.next}},
			}},
			Select: []string{"time"},
			Order:  []client.Order{{By: "offset"}},
			Raw:    true,
			Limit:  linePage,
		})
		if err != nil {
			return total, err
		}
		for i, row := range ans.Rows {
			if i >= len(ans.Raw) || i >= len(ans.Records) {
				break
			}
			line := bytes.TrimRight([]byte(ans.Raw[i]), "\r\n")
			var at time.Time
			if len(row) > 0 {
				at = parseTime(row[0])
			}
			fs.fold(line, ans.Records[i].Offset, at)
			r.linesFolded++
			total++
		}
		if !ans.More || len(ans.Rows) == 0 {
			return total, nil
		}
	}
}
