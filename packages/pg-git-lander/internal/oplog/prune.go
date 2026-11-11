package oplog

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
)

// Retention of the operation logs (design section 14.5).
const (
	RetentionAge   = 14 * 24 * time.Hour
	RetentionBytes = int64(200) << 20
)

// Prune removes the logs older than RetentionAge and then, oldest first,
// enough logs to bring the total under RetentionBytes. It never removes the
// log of an op id in protected (the op ids lock records name); a protected log
// still counts toward the total. It returns the op ids removed. The CLI calls
// it at the start of each invocation.
func Prune(commonDir string, now time.Time, protected map[string]bool) ([]string, error) {
	return prune(commonDir, now, protected, RetentionAge, RetentionBytes)
}

func prune(commonDir string, now time.Time, protected map[string]bool, maxAge time.Duration, maxBytes int64) ([]string, error) {
	dir := LogsDir(commonDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type lg struct {
		id   string
		size int64
		mt   time.Time
	}
	var all []lg
	var total int64
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		all = append(all, lg{strings.TrimSuffix(e.Name(), ".log"), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mt.Before(all[j].mt) })
	var removed []string
	for _, l := range all {
		if protected[l.id] {
			continue
		}
		if now.Sub(l.mt) <= maxAge && total <= maxBytes {
			continue
		}
		if err := os.Remove(filepath.Join(dir, l.id+".log")); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		total -= l.size
		removed = append(removed, l.id)
	}
	return removed, nil
}

// NamedOpIDs returns the op ids that lock records currently name: the logs
// Prune must keep. A record that cannot be read names nothing it can be
// asked about.
func NamedOpIDs(ctx context.Context, l *locks.Layer) (map[string]bool, error) {
	r := l.Git.Run(ctx, l.Dir, "for-each-ref", "--format=%(refname)", locks.RefPrefix)
	if !r.OK() {
		return nil, &refListError{stderr: strings.TrimSpace(string(r.Stderr))}
	}
	named := map[string]bool{}
	for _, ref := range strings.Fields(string(r.Stdout)) {
		st, err := l.Read(ctx, ref)
		if err != nil {
			return nil, err
		}
		if st.Rec != nil && st.Rec.OpID != "" {
			named[st.Rec.OpID] = true
		}
	}
	return named, nil
}

type refListError struct{ stderr string }

func (e *refListError) Error() string { return "git for-each-ref: " + e.stderr }

// PruneNamed is the call made at the start of each invocation: it asks the
// lock layer which op ids records name and prunes around them.
func PruneNamed(ctx context.Context, l *locks.Layer, now time.Time) ([]string, error) {
	common, err := l.Git.CommonDir(ctx, l.Dir)
	if err != nil {
		return nil, err
	}
	named, err := NamedOpIDs(ctx, l)
	if err != nil {
		return nil, err
	}
	return Prune(common, now, named)
}
