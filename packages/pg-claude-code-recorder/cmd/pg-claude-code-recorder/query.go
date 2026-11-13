package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// readRequest reads the request JSON from --request FILE or standard input
// and applies --limit.
func (a *app) readRequest(file string, limit int) (map[string]any, error) {
	var data []byte
	var err error
	if file != "" {
		data, err = os.ReadFile(file)
	} else {
		data, err = io.ReadAll(a.stdin)
	}
	if err != nil {
		return nil, err
	}
	req := map[string]any{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, fmt.Errorf("request is not a JSON object: %v", err)
		}
	}
	if limit > 0 {
		req["limit"] = limit
	}
	return req, nil
}

// request runs op with a request body for the query and search subcommands.
func (a *app) request(op string, args []string) int {
	var c common
	var file string
	var limit int
	var jsonl bool
	fs := newFlagSet(op, a.errw)
	c.bind(fs)
	fs.StringVar(&file, "request", "", "read the request JSON from FILE instead of standard input")
	fs.IntVar(&limit, "limit", 0, "most rows or hits to return (default 1000, at most 10000)")
	fs.BoolVar(&jsonl, "jsonl", false, "print one JSON object per row or hit")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	req, err := a.readRequest(file, limit)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrBadConfig, err.Error())
	}
	var ans map[string]any
	if err := cl.Post(a.ctx, op, req, &ans); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	a.printAnswer(op, ans, jsonl)
	if d, _ := ans["degraded"].(bool); d && !c.partialOK {
		return a.emitErrorFor(c.jsonOut, wire.ErrBehind, "the recorder is still behind (first backfill)", cfg.Socket)
	}
	return wire.ExitOK
}

func (a *app) printAnswer(op string, ans map[string]any, jsonl bool) {
	if !jsonl {
		b, _ := json.Marshal(ans)
		_, _ = fmt.Fprintln(a.out, string(b))
		return
	}
	if op == "search" {
		hits, _ := ans["hits"].([]any)
		for _, h := range hits {
			b, _ := json.Marshal(h)
			_, _ = fmt.Fprintln(a.out, string(b))
		}
		return
	}
	cols, _ := ans["columns"].([]any)
	rows, _ := ans["rows"].([]any)
	for _, r := range rows {
		row, _ := r.([]any)
		obj := map[string]any{}
		for i, c := range cols {
			if i < len(row) {
				obj[fmt.Sprint(c)] = row[i]
			}
		}
		b, _ := json.Marshal(obj)
		_, _ = fmt.Fprintln(a.out, string(b))
	}
}

// find looks a Conversation up by session id (a query on the session key) and
// falls back to a word search of the text; a bead id is a text search.
func (a *app) find(args []string) int {
	var c common
	var limit int
	fs := newFlagSet("find", a.errw)
	c.bind(fs)
	fs.IntVar(&limit, "limit", 10, "most hits to return")
	if err := fs.Parse(args); err != nil {
		return a.emitError(c.jsonOut, wire.ErrUsage, err.Error())
	}
	if fs.NArg() != 1 {
		return a.emitError(c.jsonOut, wire.ErrUsage, "find takes one argument: a session id or text")
	}
	arg := fs.Arg(0)
	cfg, cl, err := a.resolve(&c)
	if err != nil {
		return a.emitError(c.jsonOut, wire.ErrBadConfig, err.Error())
	}
	var q map[string]any
	q1 := map[string]any{
		"from":      "records",
		"where":     map[string]any{"field": "session", "eq": arg},
		"group":     []string{"session"},
		"aggregate": []map[string]any{{"fn": "count", "as": "records"}, {"fn": "min", "field": "time", "as": "first"}, {"fn": "max", "field": "time", "as": "last"}},
		"limit":     1,
	}
	if err := cl.Post(a.ctx, "query", q1, &q); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	if rows, _ := q["rows"].([]any); len(rows) > 0 {
		b, _ := json.Marshal(map[string]any{"kind": "session", "answer": q})
		_, _ = fmt.Fprintln(a.out, string(b))
		return wire.ExitOK
	}
	var s map[string]any
	if err := cl.Post(a.ctx, "search", map[string]any{"mode": "word", "q": arg, "limit": limit}, &s); err != nil {
		return a.emitWireError(c.jsonOut, err, cfg.Socket)
	}
	if hits, _ := s["hits"].([]any); len(hits) > 0 {
		b, _ := json.Marshal(map[string]any{"kind": "text", "answer": s})
		_, _ = fmt.Fprintln(a.out, string(b))
		return wire.ExitOK
	}
	return a.emitErrorFor(c.jsonOut, wire.ErrNotFound, fmt.Sprintf("no session %q and no text matching it", arg), cfg.Socket)
}

var _ = client.New
