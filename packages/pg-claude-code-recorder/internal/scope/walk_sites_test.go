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
	"internal/watcher/sweep_bulk_darwin.go:bulkReadDir": "exempt: reads one directory bulkWalk already accepted (the predicate runs in its caller)",
	"internal/watcher/watchtree.go:walkWatchTree":       "predicate: the inotify watch walk prunes with the descend argument (Scope.DescendDir)",
	"internal/watcher/backend_linux.go:addWatch":        "exempt: adds one watch for a directory walkWatchTree already accepted (the predicate runs in its caller)",
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
	"internal/copier/copier.go:recoverLocked":        "exempt: lists the recorder's own copies directory (state, not the Claude directory)",
	"internal/copier/privatescan.go:PrivateScanStep": "exempt: walks the recorder's own copies directory incrementally (state, not the Claude directory)",
	"internal/settle/settle.go:Recover":              "exempt: lists the recorder's own archives directory (state, not the Claude directory)",
	"internal/serve/disk.go:DirBytes":                "exempt: sizes the recorder's own state directory",
	// generic seams
	"internal/fsx/fsx.go:ReadDir":      "exempt: the filesystem seam; its callers are on this list individually",
	"internal/fsx/recorder.go:ReadDir": "exempt: the recording wrapper of the filesystem seam",
	// test support, never run by the daemon
	"internal/invariants/invariants.go:Violations":  "exempt: test support, deliberately whole-tree (a stray quarantine or an orphan temporary file anywhere is a defect)",
	"internal/invariants/invariants.go:SnapshotDir": "exempt: test support, deliberately whole-tree so a test proves nothing in the Claude directory changed",
}

// Directory-enumerating functions by import path, and method names. A site is
// any REFERENCE to one of them (a call, a function value such as
// `f := filepath.WalkDir`, a struct field or a package-level initializer), not
// only a call.
var walkQualified = map[string]bool{
	"path/filepath.Walk": true, "path/filepath.WalkDir": true, "path/filepath.Glob": true,
	"os.ReadDir": true, "io/fs.WalkDir": true, "io/fs.ReadDir": true, "io/ioutil.ReadDir": true,
	"syscall.InotifyAddWatch": true, "golang.org/x/sys/unix.InotifyAddWatch": true,
	"syscall.ReadDirent": true, "golang.org/x/sys/unix.Getdents": true,
}
var walkMethods = map[string]bool{"ReadDir": true, "Readdir": true, "Readdirnames": true}

// native readers that appear in cgo text rather than Go code.
var nativeReaders = []string{"getattrlistbulk(", "opendir(", "readdir(", "fts_open(", "scandir("}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..") // the test runs in internal/scope
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found: %v", err)
	}
	return root
}

// declName names the top-level declaration that holds n: a function, a
// package-level var/const (its first name) or a type.
func declName(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, sp := range d.Specs {
			switch sp := sp.(type) {
			case *ast.ValueSpec:
				return sp.Names[0].Name
			case *ast.TypeSpec:
				return sp.Name.Name
			}
		}
	}
	return "decl"
}

// scanSource returns the walk sites ("rel:decl") in one Go source file.
func scanSource(t *testing.T, rel string, src []byte) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	f, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	imports := map[string]string{} // local name -> import path
	for _, im := range f.Imports {
		path := strings.Trim(im.Path.Value, `"`)
		name := path[strings.LastIndex(path, "/")+1:]
		if im.Name != nil {
			name = im.Name.Name
		}
		imports[name] = path
	}
	for _, decl := range f.Decls {
		name := declName(decl)
		ast.Inspect(decl, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			hit := walkMethods[sel.Sel.Name]
			if id, ok := sel.X.(*ast.Ident); ok {
				if path, isPkg := imports[id.Name]; isPkg {
					hit = walkQualified[path+"."+sel.Sel.Name]
				}
			}
			if hit {
				found[rel+":"+name] = true
			}
			return true
		})
	}
	if strings.Contains(string(src), `import "C"`) {
		for _, c := range f.Comments {
			for _, nr := range nativeReaders {
				if strings.Contains(c.Text(), nr) {
					found[rel+":cgo-reader"] = true
				}
			}
		}
	}
	return found
}

// funcMentionsPredicate reports whether the top-level function fn of src
// itself (its parameters or body, not the rest of the file) references the
// directory predicate: an identifier named descend, or a DescendDir selector.
func funcMentionsPredicate(t *testing.T, src []byte, fn string) (found, mentions bool) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		found = true
		ast.Inspect(fd, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && (id.Name == "descend" || id.Name == "DescendDir") {
				mentions = true
			}
			return true
		})
	}
	return found, mentions
}

func foundWalkSites(t *testing.T, root string) map[string]bool {
	t.Helper()
	found := map[string]bool{}
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
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for k := range scanSource(t, filepath.ToSlash(rel), src) {
			found[k] = true
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
		fn := site[strings.Index(site, ":")+1:]
		src, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Errorf("%s: %v", site, err)
			continue
		}
		defined, mentions := funcMentionsPredicate(t, src, fn)
		// a stale entry names a site that no longer exists. The linux file and
		// the darwin cgo file are found by the scan too (build tags do not stop
		// the parser), but a function that only calls a walker is checked by name.
		if !found[site] && !defined {
			stale = append(stale, site)
		}
		if strings.HasPrefix(reason, "predicate:") && !mentions {
			t.Errorf("%s claims to use the allowlist predicate but the function itself never references descend or DescendDir", site)
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

// Negative controls: each way of reaching a directory walk that a call-only,
// file-wide scan would miss MUST be found by the scanner.
func TestWalkGuardNegativeControls(t *testing.T) {
	cases := []struct {
		name, src, site string
	}{
		{"direct call", "package x\nimport \"path/filepath\"\nfunc w() { _ = filepath.WalkDir(\".\", nil) }\n", "x.go:w"},
		{"function value", "package x\nimport \"path/filepath\"\nfunc w() { f := filepath.WalkDir; _ = f }\n", "x.go:w"},
		{"package-level var initializer", "package x\nimport \"path/filepath\"\nvar walker = filepath.WalkDir\n", "x.go:walker"},
		{"struct field in a var initializer", "package x\nimport \"path/filepath\"\ntype t struct{ walk func(string, func(string, interface{}, error) error) error }\nvar _ = t{walk: nil}\nvar w2 = struct{ f interface{} }{f: filepath.Walk}\n", "x.go:w2"},
		{"aliased import", "package x\nimport fp \"path/filepath\"\nfunc w() { _ = fp.WalkDir(\".\", nil) }\n", "x.go:w"},
		{"method value", "package x\ntype fsys interface{ ReadDir(string) ([]int, error) }\nfunc w(f fsys) { r := f.ReadDir; _ = r }\n", "x.go:w"},
		{"inotify", "package x\nimport \"syscall\"\nfunc w() { _, _ = syscall.InotifyAddWatch(1, \"/\", 0) }\n", "x.go:w"},
	}
	for _, c := range cases {
		if !scanSource(t, "x.go", []byte(c.src))[c.site] {
			t.Errorf("%s: the scanner missed %s", c.name, c.site)
		}
	}
	// a file that only imports the package and a non-walking function is clean
	if got := scanSource(t, "x.go", []byte("package x\nimport \"path/filepath\"\nfunc w() string { return filepath.Join(\"a\", \"b\") }\n")); len(got) != 0 {
		t.Errorf("false positive: %v", got)
	}
}

// A predicate: entry is only believed when the walking function itself
// references the predicate, not when the word appears elsewhere in the file.
func TestWalkGuardPredicateMustBeInTheWalkingFunction(t *testing.T) {
	src := []byte("package x\n" +
		"import \"path/filepath\"\n" +
		"// descend is mentioned in this comment and in the other function\n" +
		"func other(descend func(string) bool) bool { return descend(\"\") }\n" +
		"func walker() { _ = filepath.WalkDir(\".\", nil) }\n" +
		"func good(descend func(string) bool) { _ = filepath.WalkDir(\".\", nil); _ = descend }\n" +
		"func viaScope(sc interface{ DescendDir(string) bool }) { _ = filepath.WalkDir(\".\", nil); _ = sc.DescendDir }\n")
	if found, m := funcMentionsPredicate(t, src, "walker"); !found || m {
		t.Errorf("walker: found=%v mentions=%v, want found without a predicate", found, m)
	}
	if found, m := funcMentionsPredicate(t, src, "good"); !found || !m {
		t.Errorf("good: found=%v mentions=%v", found, m)
	}
	if found, m := funcMentionsPredicate(t, src, "viaScope"); !found || !m {
		t.Errorf("viaScope: found=%v mentions=%v", found, m)
	}
}
