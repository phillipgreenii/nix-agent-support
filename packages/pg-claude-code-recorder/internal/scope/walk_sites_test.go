package scope

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Architecture guard: this module once walked the whole Claude directory every
// sweep (and its inotify backend watched every directory), reading hundreds of
// thousands of files the recorder never ingests. Every place that enumerates
// directories MUST be on this list, with a reason, and anything that reaches
// into the Claude directory MUST go through the scope allowlist
// (Scope.DescendDir, the predicate the sweep and the watch walk share).
//
// A site is "<file relative to the module root>:<enclosing top-level function>".
// A reason starts with "predicate:" (the site routes through the allowlist) or
// "exempt:" (the site never reads the Claude directory, or reads exactly one
// named directory, or is test support, and why).
var approvedWalkSites = map[string]string{
	// the Claude directory walkers: all routed through the allowlist
	"internal/watcher/sweep.go:walkSweep":               "predicate: the portable sweep prunes with the descend argument (Scope.DescendDir)",
	"internal/watcher/sweep_bulk_darwin.go:bulkWalk":    "predicate: the getattrlistbulk sweep prunes with the descend argument (Scope.DescendDir)",
	"internal/watcher/sweep_bulk_darwin.go:bulkReadDir": "predicate: reads one directory the bulk walk already accepted",
	"internal/watcher/watchtree.go:walkWatchTree":       "predicate: the inotify watch walk prunes with the descend argument (Scope.DescendDir)",
	"internal/watcher/backend_linux.go:addWatch":        "predicate: adds one watch for a directory walkWatchTree already accepted",
	"internal/settle/remove.go:quarantineDirs":          "predicate: walks projects/ pruned with Scope.DescendDir (plus the quarantine directory itself)",
	// one named directory, non-recursive
	"internal/settle/extract.go:sessionExists":       "exempt: lists projects/ (an allowlisted root) one level to find a session; never recurses",
	"internal/settle/remove.go:findQuarantined":      "exempt: lists one quarantine directory beside an ingested file",
	"internal/settle/remove.go:flushArchive":         "exempt: lists one archive directory in the recorder's own state",
	"internal/settle/remove.go:logOrphans":           "exempt: lists a quarantine directory returned by quarantineDirs",
	"internal/settle/remove.go:refreshRemovalGauges": "exempt: lists a quarantine directory returned by quarantineDirs",
	"internal/lifecycle/tracker.go:Startup":          "exempt: lists sessions/ (an allowlisted root), one level, for registry files",
	"internal/lifecycle/tracker.go:sweepFifos":       "exempt: lists the control inbox directory, not the Claude directory",
	"internal/lifecycle/tracker.go:sweepEnds":        "exempt: lists the control ends directory, an allowlisted root, one level",
	// the recorder's own state directory
	"internal/copier/copier.go:recoverLocked": "exempt: lists the recorder's own copies directory (state, not the Claude directory)",
	"internal/settle/settle.go:Recover":       "exempt: lists the recorder's own archives directory (state, not the Claude directory)",
	"internal/serve/disk.go:DirBytes":         "exempt: sizes the recorder's own state directory",
	// generic seams
	"internal/fsx/fsx.go:ReadDir":      "exempt: the filesystem seam; its callers are on this list individually",
	"internal/fsx/recorder.go:ReadDir": "exempt: the recording wrapper of the filesystem seam",
	// test support, never run by the daemon
	"internal/invariants/invariants.go:Violations":  "exempt: test support, deliberately whole-tree (a stray quarantine or an orphan temporary file anywhere is a defect)",
	"internal/invariants/invariants.go:SnapshotDir": "exempt: test support, deliberately whole-tree so a test proves nothing in the Claude directory changed",
}

// directory-enumerating calls: package-qualified functions and method names.
var walkQualified = map[string]bool{
	"filepath.Walk": true, "filepath.WalkDir": true, "filepath.Glob": true,
	"os.ReadDir": true, "os.ReadFile_": false, "fs.WalkDir": true, "fs.ReadDir": true,
	"ioutil.ReadDir": true, "syscall.InotifyAddWatch": true, "unix.InotifyAddWatch": true,
	"syscall.ReadDirent": true, "unix.Getdents": true,
}
var walkMethods = map[string]bool{"ReadDir": true, "Readdir": true, "Readdirnames": true}

// native readers that appear in cgo text rather than Go calls.
var nativeReaders = []string{"getattrlistbulk(", "opendir(", "readdir(", "fts_open(", "scandir("}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..") // the test runs in internal/scope
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found: %v", err)
	}
	return root
}

func foundWalkSites(t *testing.T, root string) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "testdata" || n == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, p, src, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				hit := walkMethods[sel.Sel.Name]
				if id, ok := sel.X.(*ast.Ident); ok && walkQualified[id.Name+"."+sel.Sel.Name] {
					hit = true
				}
				if hit {
					found[rel+":"+fn.Name.Name] = true
				}
				return true
			})
		}
		// native directory readers live in cgo comment text
		if strings.Contains(string(src), `import "C"`) {
			for _, c := range f.Comments {
				for _, nr := range nativeReaders {
					if !strings.Contains(c.Text(), nr) {
						continue
					}
					// attribute it to the first function after "C" use: name by file
					found[rel+":cgo-reader"] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestEveryDirectoryWalkIsApproved(t *testing.T) {
	root := moduleRoot(t)
	found := foundWalkSites(t, root)
	// the cgo reader is covered by its Go callers (bulkWalk, bulkReadDir)
	delete(found, "internal/watcher/sweep_bulk_darwin.go:cgo-reader")

	var unapproved, stale []string
	for site := range found {
		if _, ok := approvedWalkSites[site]; !ok {
			unapproved = append(unapproved, site)
		}
	}
	for site, reason := range approvedWalkSites {
		if !strings.HasPrefix(reason, "predicate:") && !strings.HasPrefix(reason, "exempt:") {
			t.Errorf("%s: the reason must start with predicate: or exempt:", site)
		}
		file := site[:strings.Index(site, ":")]
		src, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Errorf("%s: %v", site, err)
			continue
		}
		fn := site[strings.Index(site, ":")+1:]
		// a stale entry names a site that no longer exists. The _linux file
		// and the darwin cgo file are only found by the source scan below.
		if !found[site] && !strings.Contains(string(src), "func "+fn+"(") && !strings.Contains(string(src), ") "+fn+"(") {
			stale = append(stale, site)
		}
		if strings.HasPrefix(reason, "predicate:") &&
			!strings.Contains(string(src), "descend") && !strings.Contains(string(src), "DescendDir") {
			t.Errorf("%s claims to use the allowlist predicate but the file never mentions it", site)
		}
	}
	sort.Strings(unapproved)
	sort.Strings(stale)
	if len(unapproved) > 0 {
		t.Errorf("directory walk sites not on the approved list in walk_sites_test.go:\n  %s\n"+
			"A walk that reads the Claude directory MUST be routed through the scope allowlist: prune with Scope.DescendDir "+
			"(see watcher.walkSweep and watcher.walkWatchTree) so it never opens a tree the recorder does not ingest "+
			"(file-history, plugins, spikes-*, ...). If the site reads only one named directory, or never touches the Claude directory, "+
			"add it to approvedWalkSites with a one-line exempt: reason.", strings.Join(unapproved, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("approved walk sites that no longer exist (remove them):\n  %s", strings.Join(stale, "\n  "))
	}
}

// The guard itself must see the walkers it protects.
func TestWalkGuardSeesTheKnownWalkers(t *testing.T) {
	found := foundWalkSites(t, moduleRoot(t))
	for _, want := range []string{
		"internal/watcher/sweep.go:walkSweep",
		"internal/watcher/watchtree.go:walkWatchTree",
		"internal/settle/remove.go:quarantineDirs",
		"internal/invariants/invariants.go:SnapshotDir",
	} {
		if !found[want] {
			t.Errorf("the scan missed %s", want)
		}
	}
	if !found["internal/watcher/sweep_bulk_darwin.go:cgo-reader"] {
		t.Error("the scan missed the getattrlistbulk reader")
	}
}
