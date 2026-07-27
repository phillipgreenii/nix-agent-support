package query

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

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

type rawItem struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata"`
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
	items := make([]item.Item, 0, len(raws))
	for _, r := range raws {
		if r.ID == "" {
			return nil, fmt.Errorf("command query: record missing required \"id\"")
		}
		items = append(items, item.Item{ID: r.ID, Type: r.Type, Title: r.Title, Metadata: r.Metadata})
	}
	return eventsFromItems(items, firstEmit(q), ""), nil
}

// OSCommander is the default Commander: shells out via os/exec.
type OSCommander struct{}

func (OSCommander) Run(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}
