package patheval

import (
	"os"
	"sync"
)

// realPathMemo is an OPT-IN, process-wide memo of evalSymlinksWithFallback.
//
// Why it exists: one Evaluate resolves a few dozen reference and zone-root
// paths (/tmp, /nix, $HOME and its XDG/gradle/go subpaths, the cwd, ...), and
// every resolution is a chain of lstat calls (a path that does not exist, as
// most of a hermetic $HOME's subpaths do not, additionally re-walks its
// ancestors one prefix at a time). A production hook process evaluates once,
// so the cost is paid once. A test that evaluates 10^4 joint commands against
// a filesystem that cannot change pays it 10^4 times, and under host load the
// lstat latency, not CPU, dominates (pg2-1w4c4n). Such a test turns the memo
// on for its own duration with MemoizeRealPaths.
//
// It is off by default and MUST stay off in production code: a memo would
// serve a stale answer if a symlink changed under a long-lived process.
//
// The same switch also memoizes Exists, the existence probe behind the
// ancestor walks (the `.git` search and the workspace-marker search), which
// were the next-largest source of per-Evaluate stat calls.
var realPathMemo struct {
	mu     sync.Mutex
	m      map[string]string // nil while the memo is off
	exists map[string]bool
}

// MemoizeRealPaths turns the realpath and existence memos on (with empty
// tables) and returns a function that turns them off and drops the tables. The
// caller MUST guarantee that the filesystem it resolves against does not
// change while the memo is on. It is a test seam; it is not safe to nest or to
// overlap with another holder.
func MemoizeRealPaths() (restore func()) {
	realPathMemo.mu.Lock()
	realPathMemo.m = map[string]string{}
	realPathMemo.exists = map[string]bool{}
	realPathMemo.mu.Unlock()
	return func() {
		realPathMemo.mu.Lock()
		realPathMemo.m = nil
		realPathMemo.exists = nil
		realPathMemo.mu.Unlock()
	}
}

// Exists reports whether os.Stat(path) succeeds (the path exists, following
// symlinks). Production callers get a plain stat; with MemoizeRealPaths on, a
// path is stat-ed once.
func Exists(path string) bool {
	realPathMemo.mu.Lock()
	memo := realPathMemo.exists != nil
	if memo {
		if v, ok := realPathMemo.exists[path]; ok {
			realPathMemo.mu.Unlock()
			return v
		}
	}
	realPathMemo.mu.Unlock()

	_, err := os.Stat(path)
	ok := err == nil
	if memo {
		realPathMemo.mu.Lock()
		if realPathMemo.exists != nil {
			realPathMemo.exists[path] = ok
		}
		realPathMemo.mu.Unlock()
	}
	return ok
}

func memoLookup(path string) (string, bool) {
	realPathMemo.mu.Lock()
	defer realPathMemo.mu.Unlock()
	if realPathMemo.m == nil {
		return "", false
	}
	v, ok := realPathMemo.m[path]
	return v, ok
}

func memoStore(path, resolved string) {
	realPathMemo.mu.Lock()
	defer realPathMemo.mu.Unlock()
	if realPathMemo.m != nil {
		realPathMemo.m[path] = resolved
	}
}
