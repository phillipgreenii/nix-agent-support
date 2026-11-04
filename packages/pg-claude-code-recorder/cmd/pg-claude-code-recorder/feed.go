package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// bodyFlags are the request fields feed and snapshot take as flags.
type bodyFlags struct {
	file, where, project string
	limit                int
}

func (b *bodyFlags) request(base map[string]any) (map[string]any, error) {
	req := base
	if b.file != "" {
		data, err := os.ReadFile(b.file)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, fmt.Errorf("request is not a JSON object: %v", err)
		}
	}
	if b.where != "" {
		var w any
		if err := json.Unmarshal([]byte(b.where), &w); err != nil {
			return nil, fmt.Errorf("--where is not JSON: %v", err)
		}
		req["where"] = w
	}
	if b.project != "" {
		req["project"] = strings.Split(b.project, ",")
	}
	if b.limit > 0 {
		req["limit"] = b.limit
	}
	return req, nil
}

func (a *app) printEntries(ans map[string]any, jsonl bool) {
	if !jsonl {
		b, _ := json.Marshal(ans)
		_, _ = fmt.Fprintln(a.out, string(b))
		return
	}
	entries, _ := ans["entries"].([]any)
	for _, en := range entries {
		b, _ := json.Marshal(en)
		_, _ = fmt.Fprintln(a.out, string(b))
	}
}

// feed asks the change feed once, optionally waiting for a match.
func (a *app) feed(args []string) int {
	var c common
	var b bodyFlags
	var after string
	var wait time.Duration
	var jsonl bool
	fs := newFlagSet("feed", a.errw)
	c.bind(fs)
	fs.StringVar(&b.file, "request", "", "read the whole request JSON from FILE")
	fs.StringVar(&b.where, "where", "", "the filter, as JSON")
	fs.StringVar(&b.project, "project", "", "comma-separated fields each record entry carries")
	fs.IntVar(&b.limit, "limit", 0, "most entries per page (default 1000, at most 10000)")
	fs.StringVar(&after, "after", "", "the cursor to read after (empty: from the retention floor)")
	fs.DurationVar(&wait, "wait", 0, "hold an empty feed up to this long (at most 60s)")
	fs.BoolVar(&jsonl, "jsonl", false, "print one JSON object per entry")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	req, err := b.request(map[string]any{})
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	if after != "" {
		req["after"] = after
	}
	if wait > 0 {
		req["wait_ms"] = int(wait / time.Millisecond)
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	cl.HTTP.Timeout = wait + 30*time.Second
	var ans map[string]any
	if err := cl.Post(a.ctx, "feed", req, &ans); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	a.printEntries(ans, jsonl)
	if t, _ := ans["timed_out"].(bool); t {
		return wire.ExitTimeout
	}
	if d, _ := ans["degraded"].(bool); d && !c.partialOK {
		return a.emitErrorFor(c.jsonOut, wire.ErrBehind, "the recorder is still behind (first backfill)", cfg.Socket)
	}
	return wire.ExitOK
}

// snapshot prints the state at a cursor; --all follows every page.
func (a *app) snapshot(args []string) int {
	var c common
	var b bodyFlags
	var all, jsonl bool
	fs := newFlagSet("snapshot", a.errw)
	c.bind(fs)
	fs.StringVar(&b.file, "request", "", "read the whole request JSON from FILE")
	fs.StringVar(&b.where, "where", "", "the filter, as JSON")
	fs.StringVar(&b.project, "project", "", "comma-separated fields each record entry carries")
	fs.IntVar(&b.limit, "limit", 0, "most entries per page (default 1000, at most 10000)")
	fs.BoolVar(&all, "all", false, "follow every page and print one merged answer")
	fs.BoolVar(&jsonl, "jsonl", false, "print one JSON object per entry")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	req, err := b.request(map[string]any{})
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	var merged map[string]any
	for {
		var ans map[string]any
		if err := cl.Post(a.ctx, "snapshot", req, &ans); err != nil {
			return a.emitWireError(c.jsonOut, err, cfg.Socket)
		}
		if merged == nil {
			merged = ans
		} else {
			e1, _ := merged["entries"].([]any)
			e2, _ := ans["entries"].([]any)
			merged["entries"] = append(e1, e2...)
			merged["more"], merged["next_page"] = ans["more"], ans["next_page"]
		}
		more, _ := ans["more"].(bool)
		if !all || !more {
			break
		}
		req["at"], req["page"] = merged["cursor"], ans["next_page"]
	}
	a.printEntries(merged, jsonl)
	if d, _ := merged["degraded"].(bool); d && !c.partialOK {
		return a.emitErrorFor(c.jsonOut, wire.ErrBehind, "the recorder is still behind (first backfill)", cfg.Socket)
	}
	return wire.ExitOK
}

// raw prints the bytes of a record (--record f_N:gen:offset:item) or a file range.
func (a *app) raw(args []string) int {
	var c common
	var rec, file string
	var gen, length int
	var offset int64
	fs := newFlagSet("raw", a.errw)
	c.bind(fs)
	fs.StringVar(&rec, "record", "", "a record as FILE:GEN:OFFSET:ITEM (the file as answers show it, f_12)")
	fs.StringVar(&file, "file", "", "a file (f_12) to read a range of; needs --length")
	fs.IntVar(&gen, "gen", 0, "the generation (default: the current one)")
	fs.Int64Var(&offset, "offset", 0, "the range's first byte")
	fs.IntVar(&length, "length", 0, "the range's length in bytes")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	req := map[string]any{}
	switch {
	case rec != "":
		p := strings.Split(rec, ":")
		if len(p) != 4 {
			return a.emitError(c.jsonOut, wire.ErrUsage, "--record is FILE:GEN:OFFSET:ITEM, for example f_12:1:0:-1")
		}
		g, e1 := strconv.Atoi(p[1])
		o, e2 := strconv.ParseInt(p[2], 10, 64)
		it, e3 := strconv.Atoi(p[3])
		if e1 != nil || e2 != nil || e3 != nil {
			return a.emitError(c.jsonOut, wire.ErrUsage, "--record numbers are not integers")
		}
		req["record"] = map[string]any{"file": p[0], "gen": g, "offset": o, "item": it}
	case file != "":
		req["file"], req["gen"], req["offset"], req["length"] = file, gen, offset, length
	default:
		return a.emitError(c.jsonOut, wire.ErrUsage, "raw needs --record or --file with --length")
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	var ans map[string]any
	if err := cl.Post(a.ctx, "raw", req, &ans); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	if c.jsonOut {
		b, _ := json.Marshal(ans)
		_, _ = fmt.Fprintln(a.out, string(b))
		return wire.ExitOK
	}
	data, _ := ans["data"].(string)
	if enc, _ := ans["encoding"].(string); enc == "base64" {
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return a.emitError(false, wire.ErrInternal, "undecodable answer: "+err.Error())
		}
		_, _ = a.out.Write(raw)
		return wire.ExitOK
	}
	_, _ = fmt.Fprint(a.out, data)
	return wire.ExitOK
}

// rebuild rebuilds index.db from the journal and re-parses every copy, offline.
func (a *app) rebuild(args []string) int {
	var c common
	fs := newFlagSet("rebuild", a.errw)
	c.bind(fs)
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	var h wire.Hello
	if cl.Do(a.ctx, "GET", "/v1/hello", nil, &h) == nil {
		return a.emitErrorFor(c.jsonOut, wire.ErrBusy, "the daemon is running on "+cfg.Socket+"; stop it first, or restart it with serve --rebuild", cfg.Socket)
	}
	d, err := serve.Open(serve.Options{Config: cfg, Logger: serve.NewLogger(a.errw), Version: Version, Rebuild: true, BulkRebuild: true})
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUnavailable, fmt.Sprintf("cannot rebuild: %v", err))
	}
	perr := d.Parser().ParseAll()
	if perr == nil {
		// Clears the unfinished-rebuild marker; on any failure it stays and
		// the next start refuses this index until the rebuild is rerun.
		perr = d.Store().FinishRebuild()
	}
	epoch := d.Store().Epoch()
	if err := d.Close(); err != nil && perr == nil {
		perr = err
	}
	if perr != nil {
		return a.emitError(c.jsonOut, wire.ErrInternal, "re-parse: "+perr.Error()+" (index.db is left marked as an unfinished rebuild; run rebuild again)")
	}
	_, _ = fmt.Fprintf(a.out, "rebuilt index.db in %s: epoch %d; clients with an older cursor resync from snapshot\n", cfg.StateDir, epoch)
	return wire.ExitOK
}

var (
	_ = config.Instance
	_ context.Context
)
