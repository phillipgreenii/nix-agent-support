package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
	"github.com/phillipgreenii/pg-checkout-pool/internal/provision"
)

// Exit codes. 0 is success; 1 is never emitted; every failure is 2 or more.
const (
	exitUsage          = 2
	exitExhausted      = 3
	exitLowDisk        = 4
	exitRefUnresolved  = 5
	exitQuarantineOnly = 6
	exitWedged         = 7
	exitNoLease        = 8
	exitInternal       = 9
	exitBusy           = 10
	exitOwnerHasLease  = 11
)

// Errors raised by the command layer.
var (
	errLowDisk        = errors.New("free space is below the floor; the pool will not grow")
	errQuarantineOnly = errors.New("every slot is quarantined or retired")
	errNoLease        = errors.New("no active lease with that id")
	errOwnerHasLease  = errors.New("the owner session already holds a lease")
)

const (
	defaultTTL      = 30 * time.Minute
	defaultWait     = 2 * time.Minute
	defaultMaxSlots = 6
	defaultMinFree  = 40 << 30
	waitPoll        = time.Second
	rescuePrefix    = "refs/pg-checkout-pool/rescue/"
)

// app carries the injectable parts of the CLI: clock, provisioner factory,
// sleeping and free-space probing.
type app struct {
	stdout, stderr io.Writer
	clock          pool.Clock
	newProv        func(repo string, keep []string) (provision.Provisioner, error)
	sleep          func(time.Duration)
	freeBytes      func(path string) (uint64, error)
}

func newApp(stdout, stderr io.Writer, clock pool.Clock) *app {
	return &app{
		stdout: stdout, stderr: stderr, clock: clock,
		newProv: func(repo string, keep []string) (provision.Provisioner, error) {
			return provision.NewWorktree(provision.WorktreeConfig{Repo: repo, Keep: keep})
		},
		sleep:     time.Sleep,
		freeBytes: statfsFree,
	}
}

func statfsFree(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return uint64(s.Bavail) * uint64(s.Bsize), nil //nolint:gosec // block counts are non-negative
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func (a *app) newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	return fs, fs.String("root", defaultRoot(), "pool state directory (env "+rootEnv+")")
}

func (a *app) fail(err error) int {
	fmt.Fprintf(a.stderr, "pg-checkout-pool: %v\n", err)
	return exitFor(err)
}

func (a *app) usageErr(format string, v ...any) int {
	fmt.Fprintf(a.stderr, "pg-checkout-pool: "+format+"\n", v...)
	return exitUsage
}

func exitFor(err error) int {
	switch {
	case errors.Is(err, pool.ErrExhausted):
		return exitExhausted
	case errors.Is(err, errLowDisk):
		return exitLowDisk
	case errors.Is(err, provision.ErrRefUnresolvable):
		return exitRefUnresolved
	case errors.Is(err, errQuarantineOnly):
		return exitQuarantineOnly
	case errors.Is(err, provision.ErrWedged):
		return exitWedged
	case errors.Is(err, errNoLease), errors.Is(err, pool.ErrNotLeased):
		return exitNoLease
	case errors.Is(err, pool.ErrBusy):
		return exitBusy
	case errors.Is(err, errOwnerHasLease):
		return exitOwnerHasLease
	}
	return exitInternal
}

func (a *app) emit(v any) int {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return a.fail(err)
	}
	return 0
}

// treePath is where a slot's checkout lives.
func treePath(root, slot string) string { return filepath.Join(root, "slots", slot, "tree") }

func rescueRef(leaseID string) string { return rescuePrefix + leaseID }

// validBranch is a conservative subset of git's ref-name rules, so a bad name
// is a usage error rather than a reset failure that would quarantine slots.
func validBranch(b string) bool {
	if b == "" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") ||
		strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") || strings.Contains(b, "..") ||
		strings.Contains(b, "//") || strings.Contains(b, "@{") || b == "@" {
		return false
	}
	for _, r := range b {
		if r < 0x21 || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	return true
}

type leaseOut struct {
	LeaseID     string    `json:"leaseId"`
	Owner       string    `json:"owner"`
	Slot        string    `json:"slot"`
	Path        string    `json:"path"`
	Branch      string    `json:"branch"`
	Head        string    `json:"head"`
	ExpiresAt   time.Time `json:"expiresAt"`
	ResetMillis int64     `json:"resetMillis"`
	Daemon      string    `json:"daemon"`
}

func (a *app) lease(args []string) int {
	fs, root := a.newFlags("lease")
	repo := fs.String("repo", "", "absolute path of the canonical clone")
	ref := fs.String("ref", "", "commit reference to check out")
	owner := fs.String("owner", "", "owner session id (the lease key)")
	bead := fs.String("bead", "", "work item id: an affinity hint only, never a key")
	branch := fs.String("branch", "", "lease branch (default pg-checkout-pool/<owner>)")
	ttl := fs.Duration("ttl", defaultTTL, "lease time to live")
	wait := fs.Duration("wait", defaultWait, "how long to wait for a free slot")
	maxSlots := fs.Int("max-slots", defaultMaxSlots, "pool size cap")
	minFree := fs.Uint64("min-free-bytes", defaultMinFree, "free-space floor below which the pool will not grow")
	var keep multiFlag
	fs.Var(&keep, "keep", "ignored path that survives a reset (repeatable; default is the built-in list)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	switch {
	case *owner == "":
		return a.usageErr("lease: --owner (the session id) is required")
	case *repo == "" || !filepath.IsAbs(*repo):
		return a.usageErr("lease: --repo must be an absolute path")
	case *ref == "":
		return a.usageErr("lease: --ref is required")
	case *ttl <= 0 || *wait < 0 || *maxSlots < 1:
		return a.usageErr("lease: --ttl must be positive, --wait non-negative and --max-slots at least 1")
	}
	br := *branch
	if br == "" {
		br = "pg-checkout-pool/" + *owner
	}
	if !validBranch(br) {
		return a.usageErr("lease: %q is not a usable branch name", br)
	}
	prov, err := a.newProv(*repo, keep)
	if err != nil {
		return a.usageErr("lease: %v", err)
	}
	st := pool.NewStore(*root, a.clock)
	ctx := context.Background()

	if _, held, err := st.FindByOwner(*owner); err != nil {
		return a.fail(err)
	} else if held {
		return a.fail(fmt.Errorf("%w: %s", errOwnerHasLease, *owner))
	}
	commit, err := prov.Resolve(ctx, *ref)
	if err != nil {
		return a.fail(err)
	}
	req := pool.AcquireRequest{OwnerSessionID: *owner, BeadHint: *bead, Repo: *repo, Commit: commit, Branch: br, TTL: *ttl}
	deadline := a.clock.Now().Add(*wait)
	created := map[string]bool{}
	for {
		h, err := st.Acquire(req)
		if err == nil {
			out, retry, err := a.finishLease(ctx, prov, h, *root, req, created)
			if err != nil {
				return a.fail(err)
			}
			if !retry {
				return a.emit(out)
			}
			continue
		}
		if !errors.Is(err, pool.ErrExhausted) {
			return a.fail(err)
		}
		grew, err := a.grow(ctx, prov, st, *root, commit, *maxSlots, *minFree, created)
		if err != nil {
			return a.fail(err)
		}
		if grew {
			continue
		}
		if quarantineOnly(st) {
			return a.fail(errQuarantineOnly)
		}
		remaining := deadline.Sub(a.clock.Now())
		if remaining <= 0 {
			return a.fail(fmt.Errorf("%w (waited %s)", pool.ErrExhausted, *wait))
		}
		a.sleep(min(waitPoll, remaining))
	}
}

// finishLease resets the acquired slot to the commit and marks it leased.
// retry is true when the slot proved unusable and was quarantined, so the
// caller should try another.
func (a *app) finishLease(ctx context.Context, prov provision.Provisioner, h *pool.Handle, root string, req pool.AcquireRequest, created map[string]bool) (leaseOut, bool, error) {
	defer h.Unlock()
	rec := h.Record()
	dir := treePath(root, rec.Slot)
	start := a.clock.Now()
	if err := prov.Verify(ctx, dir); err != nil {
		return a.unusable(h, err)
	}
	head, err := a.resetTwice(ctx, func() (string, error) { return prov.Reset(ctx, dir, req.Commit, req.Branch) })
	if err != nil {
		return a.unusable(h, err)
	}
	if err := h.MarkLeased(); err != nil {
		return leaseOut{}, false, err
	}
	rec = h.Record()
	daemon := "warm"
	if created[rec.Slot] {
		daemon = "cold"
	}
	return leaseOut{
		LeaseID: rec.LeaseID, Owner: rec.OwnerSessionID, Slot: rec.Slot, Path: dir, Branch: rec.Branch,
		Head: head, ExpiresAt: rec.Expiry, ResetMillis: a.clock.Now().Sub(start).Milliseconds(), Daemon: daemon,
	}, false, nil
}

// unusable quarantines a slot that failed Verify or Reset. A wedged git is
// fatal for the request; any other failure moves on to another slot.
func (a *app) unusable(h *pool.Handle, err error) (leaseOut, bool, error) {
	err = quarantineSlot(h, err)
	if errors.Is(err, provision.ErrWedged) {
		return leaseOut{}, false, err
	}
	return leaseOut{}, true, nil
}

// quarantineSlot quarantines the slot behind h for err unless it already is,
// and returns err (joined with any quarantine failure).
func quarantineSlot(h *pool.Handle, err error) error {
	err = provision.QuarantineIfWedged(h, err)
	if h.Record().State != pool.Quarantined {
		if qerr := h.Quarantine(err.Error()); qerr != nil {
			err = errors.Join(err, fmt.Errorf("quarantine: %w", qerr))
		}
	}
	return err
}

// resetTwice runs fn and retries it once: a slot is quarantined only after
// two failed resets.
func (a *app) resetTwice(_ context.Context, fn func() (string, error)) (string, error) {
	head, err := fn()
	if err == nil || errors.Is(err, provision.ErrWedged) {
		return head, err
	}
	return fn()
}

// grow adds one slot when the pool is under its cap and has disk to spare,
// serialised by a pool-wide lock. It reports whether a slot was added.
func (a *app) grow(ctx context.Context, prov provision.Provisioner, st *pool.Store, root, commit string, maxSlots int, minFree uint64, created map[string]bool) (bool, error) {
	unlock, err := lockPool(root)
	if err != nil {
		return false, err
	}
	defer unlock()
	ids, err := st.Slots()
	if err != nil {
		return false, err
	}
	active := 0
	for _, id := range ids {
		r, err := st.ReadRecord(id)
		if err != nil {
			return false, err
		}
		if r.State != pool.Retired {
			active++
		}
	}
	if active >= maxSlots {
		return false, nil
	}
	free, err := a.freeBytes(root)
	if err != nil {
		return false, err
	}
	if free < minFree {
		return false, fmt.Errorf("%w (%d bytes free, floor %d)", errLowDisk, free, minFree)
	}
	id := fmt.Sprintf("slot-%02d", len(ids)+1)
	if err := st.CreateSlot(id); err != nil {
		return false, err
	}
	h, err := st.Lock(id)
	if err != nil {
		return false, err
	}
	defer h.Unlock()
	dir := treePath(root, id)
	if err := prov.Create(ctx, dir, commit); err != nil {
		_ = os.RemoveAll(dir)
		_ = h.Retire("create failed: " + err.Error())
		return false, err
	}
	created[id] = true
	return true, nil
}

func lockPool(root string) (func(), error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, "pool.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// quarantineOnly reports whether the pool has slots but none is usable now or
// later: every one is quarantined or retired.
func quarantineOnly(st *pool.Store) bool {
	sts, err := st.Status()
	if err != nil || len(sts) == 0 {
		return false
	}
	for _, s := range sts {
		if s.Record.State != pool.Quarantined && s.Record.State != pool.Retired {
			return false
		}
	}
	return true
}

// findLease returns the slot holding lease id.
func findLease(st *pool.Store, id string) (string, error) {
	if id != "" {
		ids, err := st.Slots()
		if err != nil {
			return "", err
		}
		for _, s := range ids {
			r, err := st.ReadRecord(s)
			if err != nil {
				return "", err
			}
			if r.LeaseID == id && (r.State == pool.Leased || r.State == pool.Resetting) {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("%w: %q", errNoLease, id)
}

func (a *app) renew(args []string) int {
	fs, root := a.newFlags("renew")
	id := fs.String("lease", "", "lease id")
	ttl := fs.Duration("ttl", defaultTTL, "new time to live, from now")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *id == "" || *ttl <= 0 {
		return a.usageErr("renew: --lease is required and --ttl must be positive")
	}
	st := pool.NewStore(*root, a.clock)
	slot, err := findLease(st, *id)
	if err != nil {
		return a.fail(err)
	}
	h, err := st.Lock(slot)
	if err != nil {
		return a.fail(err)
	}
	defer h.Unlock()
	if h.Record().LeaseID != *id {
		return a.fail(fmt.Errorf("%w: %q", errNoLease, *id))
	}
	if err := h.Renew(*ttl); err != nil {
		return a.fail(err)
	}
	r := h.Record()
	return a.emit(map[string]any{"leaseId": r.LeaseID, "slot": r.Slot, "expiresAt": r.Expiry})
}

// reclaim takes a leased or resetting slot back to warm-free: capture any
// uncommitted tracked work to a rescue ref first (a failed capture quarantines
// and never resets), detach, reset to the lease commit and clear the lease.
// It returns the rescue ref when work was captured.
func (a *app) reclaim(ctx context.Context, prov provision.Provisioner, h *pool.Handle, dir string) (string, error) {
	rec := h.Record()
	if rec.State == pool.Leased {
		if err := h.BeginReset(); err != nil {
			return "", err
		}
	}
	ref := rescueRef(rec.LeaseID)
	commit, err := prov.Rescue(ctx, dir, ref)
	if err != nil {
		err = fmt.Errorf("rescue capture: %w", err)
		return "", quarantineSlot(h, err)
	}
	rescued := ""
	if commit != "" {
		rescued = ref
	}
	if err := prov.Detach(ctx, dir); err != nil {
		return rescued, quarantineSlot(h, err)
	}
	if _, err := a.resetTwice(ctx, func() (string, error) { return prov.Reset(ctx, dir, rec.Commit, "") }); err != nil {
		return rescued, quarantineSlot(h, err)
	}
	return rescued, h.FinishReset()
}

func (a *app) release(args []string) int {
	fs, root := a.newFlags("release")
	id := fs.String("lease", "", "lease id")
	disp := fs.String("disposition", "reset", "reset | keep | quarantine")
	var keep multiFlag
	fs.Var(&keep, "keep", "ignored path that survives a reset (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *id == "" {
		return a.usageErr("release: --lease is required")
	}
	switch *disp {
	case "reset", "keep", "quarantine":
	default:
		return a.usageErr("release: --disposition must be reset, keep or quarantine")
	}
	st := pool.NewStore(*root, a.clock)
	slot, err := findLease(st, *id)
	if err != nil {
		return a.fail(err)
	}
	h, err := st.Lock(slot)
	if err != nil {
		return a.fail(err)
	}
	defer h.Unlock()
	rec := h.Record()
	if rec.LeaseID != *id || rec.State != pool.Leased {
		return a.fail(fmt.Errorf("%w: %q", errNoLease, *id))
	}
	out := map[string]any{"leaseId": rec.LeaseID, "slot": rec.Slot, "disposition": *disp}
	switch *disp {
	case "keep":
		err = h.Quarantine("kept by owner: held for the operator")
	case "quarantine":
		err = h.Quarantine("released with the quarantine disposition")
	default:
		var prov provision.Provisioner
		if prov, err = a.newProv(rec.Repo, keep); err != nil {
			return a.fail(err)
		}
		var rescued string
		rescued, err = a.reclaim(context.Background(), prov, h, treePath(*root, rec.Slot))
		if rescued != "" {
			out["rescueRef"] = rescued
		}
	}
	if err != nil {
		return a.fail(err)
	}
	return a.emit(out)
}

type reapEntry struct {
	Slot      string `json:"slot"`
	Action    string `json:"action"`
	LeaseID   string `json:"leaseId,omitempty"`
	Owner     string `json:"owner,omitempty"`
	RescueRef string `json:"rescueRef,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (a *app) reap(args []string) int {
	fs, root := a.newFlags("reap")
	dry := fs.Bool("dry-run", false, "report what would be reaped, change nothing")
	grace := fs.Duration("grace", 0, "extra time past expiry before a lease is reaped")
	var keep multiFlag
	fs.Var(&keep, "keep", "ignored path that survives a reset (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	st := pool.NewStore(*root, a.clock)
	ids, err := st.Slots()
	if err != nil {
		return a.fail(err)
	}
	entries := []reapEntry{}
	failed := false
	ctx := context.Background()
	for _, id := range ids {
		h, err := st.Lock(id)
		if err != nil {
			if errors.Is(err, pool.ErrBusy) {
				continue // a live holder: not ours to reap
			}
			return a.fail(err)
		}
		rec := h.Record()
		// A leased slot is reaped once expired (plus grace). A resetting slot
		// whose lock was free was interrupted, whatever its expiry says.
		expired := !rec.Expiry.IsZero() && a.clock.Now().After(rec.Expiry.Add(*grace))
		if !(rec.State == pool.Resetting || (rec.State == pool.Leased && expired)) {
			h.Unlock()
			continue
		}
		e := reapEntry{Slot: id, LeaseID: rec.LeaseID, Owner: rec.OwnerSessionID, Action: "would-reap"}
		if !*dry {
			e.Action = "reaped"
			var prov provision.Provisioner
			prov, err = a.newProv(rec.Repo, keep)
			if err == nil {
				e.RescueRef, err = a.reclaim(ctx, prov, h, treePath(*root, id))
			}
			if err != nil {
				e.Action, e.Error = "quarantined", err.Error()
				if h.Record().State != pool.Quarantined {
					_ = h.Quarantine(err.Error())
				}
				failed = true
			}
		}
		h.Unlock()
		entries = append(entries, e)
	}
	code := a.emit(entries)
	if code == 0 && failed {
		return exitInternal
	}
	return code
}
