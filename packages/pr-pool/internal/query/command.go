package query

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

// CommandQuery runs an executable and parses its stdout into items.
type CommandQuery struct {
	Meta   `toml:"-"`
	Argv   []string    `toml:"argv"`
	Format QueryFormat `toml:"format"`
}

func (q CommandQuery) Validate() error {
	if len(q.Argv) == 0 {
		return fmt.Errorf("command query: argv is required")
	}
	if q.Format != FormatJSONL && q.Format != FormatJSON {
		return fmt.Errorf("command query: format must be jsonl or json, got %q", q.Format)
	}
	return nil
}

// BackingCommand is argv[0]: the executable this source shells out to.
func (q CommandQuery) BackingCommand() string {
	if len(q.Argv) == 0 {
		return ""
	}
	return q.Argv[0]
}

// rawItem is the per-record shape a command source's stdout decodes into.
// type keeps its pre-existing meaning (item classification, carried through to
// item.Item.Type) untouched. at/expiresAt are OPTIONAL RFC3339 timestamp
// strings, camelCase to match the event wire's own at/expiresAt (Task 1.4);
// when present they are carried onto the produced event's Attributes (the
// general "extra, type-specific fields" seam event.Event already declares) —
// the source-side message boundary that would route them into
// eventqueue.Event stays Phase 5, out of this task's scope. emit is an
// OPTIONAL event-type selector: when present it MUST be one of the query's
// declared Emits() (multi-emit); absent, the record falls back to
// firstEmit(q) — today's behavior, unchanged.
type rawItem struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Metadata  map[string]any `json:"metadata"`
	At        string         `json:"at,omitempty"`
	ExpiresAt string         `json:"expiresAt,omitempty"`
	Emit      string         `json:"emit,omitempty"`
}

func (q CommandQuery) Run(ctx context.Context, env Env) ([]event.Event, error) {
	cmd := env.Cmd
	if cmd == nil {
		cmd = OSCommander{}
	}
	out, err := cmd.Run(ctx, q.Argv)
	if err != nil {
		return nil, fmt.Errorf("command query %v: %w", q.Argv, err)
	}
	var raws []rawItem
	switch q.Format {
	case FormatJSON:
		if len(bytes.TrimSpace(out)) == 0 {
			return nil, nil
		}
		if err := json.Unmarshal(out, &raws); err != nil {
			return nil, fmt.Errorf("command query: parse json: %w", err)
		}
	default: // jsonl
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var r rawItem
			if err := json.Unmarshal(line, &r); err != nil {
				return nil, fmt.Errorf("command query: parse jsonl line: %w", err)
			}
			raws = append(raws, r)
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("command query: read output: %w", err)
		}
	}
	def := firstEmit(q)
	events := make([]event.Event, 0, len(raws))
	var errs []error
	for _, r := range raws {
		if r.ID == "" {
			return nil, fmt.Errorf("command query: record missing required \"id\"")
		}
		emitType := def
		if r.Emit != "" {
			if !declaresEmit(q, r.Emit) {
				errs = append(errs, fmt.Errorf("command query: record %q: emit %q is not among the declared emits %v", r.ID, r.Emit, q.Emits()))
				continue
			}
			emitType = r.Emit
		}
		evt := event.NewItemEvent(emitType, "", item.Item{ID: r.ID, Type: r.Type, Title: r.Title, Metadata: r.Metadata})
		if r.At != "" || r.ExpiresAt != "" {
			attrs := make(map[string]any, 2)
			if r.At != "" {
				at, perr := time.Parse(time.RFC3339, r.At)
				if perr != nil {
					errs = append(errs, fmt.Errorf("command query: record %q: parse \"at\": %w", r.ID, perr))
					continue
				}
				attrs["at"] = at
			}
			if r.ExpiresAt != "" {
				expiresAt, perr := time.Parse(time.RFC3339, r.ExpiresAt)
				if perr != nil {
					errs = append(errs, fmt.Errorf("command query: record %q: parse \"expiresAt\": %w", r.ID, perr))
					continue
				}
				attrs["expiresAt"] = expiresAt
			}
			evt.Attributes = attrs
		}
		events = append(events, evt)
	}
	if len(errs) > 0 {
		return events, errors.Join(errs...)
	}
	return events, nil
}

// OSCommander is the default Commander: shells out via os/exec.
type OSCommander struct{}

func (OSCommander) Run(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}
