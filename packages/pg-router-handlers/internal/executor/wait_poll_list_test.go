package executor

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
)

// The wait poll reads only state, live and transcript_path, so it asks ccpool for
// the facet-free list, and it asks ONCE per poll: the needs_input edge check and
// the active read share that one result.

func pollWorkingCC(extra func(*dtest.FakeCC)) *dtest.FakeCC {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	if extra != nil {
		extra(cc)
	}
	return cc
}

// pollsUntilClosed runs waitDone over a bead that reads in_progress three times
// and then closed, so the wait loop makes exactly four polls.
func pollsUntilClosed(t *testing.T, cc *dtest.FakeCC) {
	t.Helper()
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "in_progress", "in_progress", "closed"}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"); err != nil {
		t.Fatalf("waitDone: %v; updates=%v", err, bd.Updates)
	}
}

func TestWaitPoll_oneFacetFreeListPerPoll(t *testing.T) {
	cc := pollWorkingCC(nil)
	pollsUntilClosed(t, cc)
	if got := cc.LightListCalls(); got != 4 {
		t.Errorf("facet-free lists = %d, want 4 (one per poll, four polls)", got)
	}
	if got := cc.PlainListCalls(); got != 0 {
		t.Errorf("plain lists = %d, want 0: the wait poll must not need cwd or git facets", got)
	}
}

// Negative control: the old shape, state and active each reading their own list,
// is visible to the counters as two lists per poll.
func TestWaitPoll_twoSeparateReadsAreCounted(t *testing.T) {
	cc := pollWorkingCC(nil)
	e := newExec(cc, &dtest.ScriptBD{}, fastCfg())
	e.activeIn(e.pollSessions(context.Background()), "pg-router-worker-zr-w")
	stateIn(e.pollSessions(context.Background()), "pg-router-worker-zr-w")
	if got := cc.LightListCalls(); got != 2 {
		t.Fatalf("two separate reads made %d lists, want 2", got)
	}
	// ... which is what the one-list-per-poll test above would then reject: 4
	// polls of two reads each would be 8, not 4.
}

// A ccpool that rejects --no-facets: the poll falls back to the plain list, asks
// the light list only once (it remembers), and the dispatch carries on to the
// same outcome.
func TestWaitPoll_fallsBackToPlainListWhenFlagRejected(t *testing.T) {
	cc := pollWorkingCC(func(c *dtest.FakeCC) {
		c.LightErr = fmt.Errorf("%w: ccpool list: exit status 2", ccpool.ErrNoFacetsUnsupported)
	})
	pollsUntilClosed(t, cc)
	if got := cc.LightListCalls(); got != 1 {
		t.Errorf("facet-free attempts = %d, want 1 (rejection is remembered)", got)
	}
	if got := cc.PlainListCalls(); got != 4 {
		t.Errorf("plain lists = %d, want 4 (one per poll)", got)
	}
}

// Any other facet-free failure is a normal unreadable poll (keep waiting), not a
// reason to stop using the light list.
func TestWaitPoll_otherLightErrorIsAnUnreadablePoll(t *testing.T) {
	cc := pollWorkingCC(func(c *dtest.FakeCC) { c.LightErr = fmt.Errorf("ccpool list: exit status 1") })
	e := newExec(cc, &dtest.ScriptBD{}, fastCfg())
	p := e.pollSessions(context.Background())
	if p.err == nil {
		t.Fatal("poll error was swallowed")
	}
	if cc.PlainListCalls() != 0 {
		t.Errorf("a non-rejection error must not fall back to the plain list")
	}
	if !e.activeIn(p, "pg-router-worker-zr-w") {
		t.Errorf("an unreadable poll must read as still active")
	}
}

// ListLight is used at exactly one place in the module's production code: the
// wait poll (which serves both of its former sites). Every other reader of the
// pool still calls List, because they read cwd or the git facets.
func TestListLight_usedOnlyByTheWaitPoll(t *testing.T) {
	root := filepath.Join("..", "..")
	type site struct{ file, fn string }
	var sites []site
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ListLight" {
						sites = append(sites, site{filepath.ToSlash(path), fd.Name.Name})
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || !strings.HasSuffix(sites[0].file, "internal/executor/ccpool.go") || sites[0].fn != "pollSessions" {
		t.Fatalf("ListLight call sites = %v, want exactly one, in executor/ccpool.go's pollSessions", sites)
	}
}
