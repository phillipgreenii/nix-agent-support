//go:build darwin && cgo

package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// A directory with more entries than one bulk buffer holds forces several
// getattrlistbulk calls into the reused buffer; concurrent walks must each see
// the full, identical tree.
func TestBulkWalkReusedBufferLargeTreeConcurrent(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("x", 100)
	const perDir = 3000
	const dirs = 2
	var wantPaths []string
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, fmt.Sprintf("example-d%d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < perDir; i++ {
			p := filepath.Join(dir, fmt.Sprintf("%s-%05d.jsonl", long, i))
			if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			wantPaths = append(wantPaths, p)
		}
	}
	// Each record carries at least the name plus ~100 bytes of attributes, so a
	// single directory must overflow one buffer.
	if perDir*(len(long)+100) <= bulkBufSize {
		t.Fatalf("test tree too small to exceed one buffer")
	}
	sort.Strings(wantPaths)

	var wg sync.WaitGroup
	results := make([][]string, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			infos, err := platformSweep(root, nil)
			errs[i] = err
			for _, fi := range infos {
				results[i] = append(results[i], fi.Path)
			}
			sort.Strings(results[i])
		}()
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("walk %d: %v", i, errs[i])
		}
		if !reflect.DeepEqual(results[i], wantPaths) {
			t.Fatalf("walk %d: got %d paths, want %d", i, len(results[i]), len(wantPaths))
		}
	}
}
