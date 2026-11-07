// Package checkoutpool is the handler's seam to the pg-checkout-pool command:
// it leases, renews and releases pooled repository checkouts by running the
// command as an external process. It never imports the pool's own packages (a
// separate Go module); the contract is the command's JSON output and its exit
// codes (see the pg-checkout-pool usage text and tldr page).
package checkoutpool

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

// Binary is the command name looked up on PATH.
const Binary = "pg-checkout-pool"

// Exit codes of the pg-checkout-pool command that the handler distinguishes.
const (
	exitExhausted = 3
	exitLowDisk   = 4
	exitNoLease   = 8
)

// Sentinel errors, each wrapped with the command's own stderr text.
var (
	// ErrExhausted: every slot is leased and the pool is at its cap (exit 3).
	ErrExhausted = errors.New("checkout pool exhausted")
	// ErrLowDisk: the pool would not grow because free space is below its floor (exit 4).
	ErrLowDisk = errors.New("checkout pool: low disk space")
	// ErrNoLease: no active lease has that id (exit 8); on release this means
	// the lease is already gone (released or reaped), which is the goal state.
	ErrNoLease = errors.New("checkout pool: no such lease")
)

// LeaseRequest is one lease call. Owner is the lease key (the dispatched
// session's external id); Bead is an affinity hint only.
type LeaseRequest struct {
	Repo, Ref, Owner, Bead string
}

// Lease is what the command reports for a granted lease.
type Lease struct {
	ID     string
	Path   string
	Slot   string
	Branch string
}

// Client is the seam the executor and the teardown paths use; tests substitute
// a fake.
type Client interface {
	Lease(ctx context.Context, req LeaseRequest) (Lease, error)
	Renew(ctx context.Context, leaseID string) error
	// Release gives the checkout back with the command's default "reset"
	// disposition. ErrNoLease means the lease is already gone.
	Release(ctx context.Context, leaseID string) error
}

// Exec runs name with args and reports stdout, stderr and the exit code. A
// non-nil error means the process could not be run or waited on at all (not a
// non-zero exit).
type Exec func(ctx context.Context, name string, args []string) (stdout, stderr []byte, exit int, err error)

// CLI is the Client backed by the pg-checkout-pool command.
type CLI struct {
	// Root is the pool state directory ("" lets the command pick its default,
	// honouring PG_CHECKOUT_POOL_ROOT).
	Root string
	// Binary overrides the command name (empty means Binary).
	Binary string
	// Exec overrides process execution (nil means os/exec); tests inject one.
	Exec Exec
}

func (c CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Root != "" {
		args = append(args, "--root", c.Root)
	}
	bin := c.Binary
	if bin == "" {
		bin = Binary
	}
	ex := c.Exec
	if ex == nil {
		ex = osExec
	}
	out, errOut, code, err := ex(ctx, bin, args)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", bin, args[0], err)
	}
	if code == 0 {
		return out, nil
	}
	msg := strings.TrimSpace(string(errOut))
	var base error
	switch code {
	case exitExhausted:
		base = ErrExhausted
	case exitLowDisk:
		base = ErrLowDisk
	case exitNoLease:
		base = ErrNoLease
	default:
		base = fmt.Errorf("%s %s exited %d", bin, args[0], code)
	}
	return nil, fmt.Errorf("%w: %s", base, msg)
}

// leasePoolWait is how long a lease call waits for a slot. Zero: the handler
// declines a dispatch (busy) rather than holding the offer open.
const leasePoolWait = "0s"

// Lease implements Client.
func (c CLI) Lease(ctx context.Context, req LeaseRequest) (Lease, error) {
	args := []string{"lease", "--repo", req.Repo, "--ref", req.Ref, "--owner", req.Owner, "--wait", leasePoolWait}
	if req.Bead != "" {
		args = append(args, "--bead", req.Bead)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return Lease{}, err
	}
	var got struct {
		LeaseID string `json:"leaseId"`
		Slot    string `json:"slot"`
		Path    string `json:"path"`
		Branch  string `json:"branch"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		return Lease{}, fmt.Errorf("lease output: %w", err)
	}
	if got.LeaseID == "" || got.Path == "" {
		return Lease{}, fmt.Errorf("lease output has no leaseId or path: %s", bytes.TrimSpace(out))
	}
	return Lease{ID: got.LeaseID, Path: got.Path, Slot: got.Slot, Branch: got.Branch}, nil
}

// Renew implements Client.
func (c CLI) Renew(ctx context.Context, leaseID string) error {
	_, err := c.run(ctx, "renew", "--lease", leaseID)
	return err
}

// Release implements Client.
func (c CLI) Release(ctx context.Context, leaseID string) error {
	_, err := c.run(ctx, "release", "--lease", leaseID)
	return err
}

func osExec(ctx context.Context, name string, args []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return so.Bytes(), se.Bytes(), ee.ExitCode(), nil
	}
	return so.Bytes(), se.Bytes(), 0, err
}
