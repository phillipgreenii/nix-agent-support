package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner runs one command and returns its stdout, and the exit code when the
// command ran and exited non-zero.
type Runner func(ctx context.Context, argv []string) (stdout []byte, exitCode int, err error)

// BD is a Tracker backed by the bd command line.
type BD struct {
	Run     Runner
	Timeout time.Duration
	// Actor is the identity every write carries; empty means DefaultActor.
	Actor string
}

// DefaultActor is the write identity when none was resolved.
const DefaultActor = "pg-rescue-triage"

// WithActor returns a copy of b whose writes all carry actor (empty keeps the
// default), so the claim and the later close, release and hand-over agree: bd
// refuses a write whose actor differs from the claimant.
func (b BD) WithActor(actor string) Tracker {
	b.Actor = actor
	return b
}

func (b BD) actor() string {
	if b.Actor == "" {
		return DefaultActor
	}
	return b.Actor
}

// ExecRunner runs argv for real.
func ExecRunner(ctx context.Context, argv []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), ee.ExitCode(), fmt.Errorf("%s: %w: %s", strings.Join(argv[:2], " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), 0, err
}

func (b BD) do(argv ...string) ([]byte, int, error) {
	t := b.Timeout
	if t <= 0 {
		t = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	return b.Run(ctx, append([]string{"bd"}, argv...))
}

// decodeData reads bd's --json output: either {"data": ...} or the bare value.
func decodeData(raw []byte, v any) error {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, v)
	}
	return json.Unmarshal(raw, v)
}

// Show implements Tracker.
func (b BD) Show(id string) (Item, error) {
	out, _, err := b.do("show", id, "--json")
	if err != nil {
		return Item{}, err
	}
	var items []struct {
		ID       string         `json:"id"`
		Status   string         `json:"status"`
		Labels   []string       `json:"labels"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := decodeData(out, &items); err != nil || len(items) == 0 {
		return Item{}, fmt.Errorf("bd show %s: unreadable reply", id)
	}
	i := items[0]
	return Item{ID: i.ID, Status: i.Status, Labels: i.Labels, Metadata: i.Metadata}, nil
}

// Comments implements Tracker, oldest first.
func (b BD) Comments(id string) ([]string, error) {
	out, _, err := b.do("comments", id, "--json")
	if err != nil {
		return nil, err
	}
	var cs []struct {
		Text string `json:"text"`
	}
	if err := decodeData(out, &cs); err != nil {
		return nil, fmt.Errorf("bd comments %s: unreadable reply", id)
	}
	texts := make([]string, len(cs))
	for i, c := range cs {
		texts[i] = c.Text
	}
	return texts, nil
}

// Claim implements Tracker; a refused claim is ErrPeerHeld.
func (b BD) Claim(id, actor string) error {
	_, code, err := b.do("update", id, "--claim", "--actor", actor)
	if err != nil && code != 0 {
		return fmt.Errorf("%w: %v", ErrPeerHeld, err)
	}
	return err
}

// Comment implements Tracker.
func (b BD) Comment(id, text string) error {
	_, _, err := b.do("comments", "add", id, "--actor", b.actor(), text)
	return err
}

// Close implements Tracker.
func (b BD) Close(id, reason string) error {
	_, _, err := b.do("close", id, "--reason", reason, "--actor", b.actor())
	return err
}

// Release implements Tracker: the status change and the assignee clear are
// one call.
func (b BD) Release(id string) error {
	_, _, err := b.do("update", id, "--status", "open", "--assignee", "", "--actor", b.actor())
	return err
}

// HandOver implements Tracker.
func (b BD) HandOver(id string) error {
	_, _, err := b.do("update", id, "--status", "open", "--assignee", "", "--add-label", "human", "--actor", b.actor())
	return err
}
