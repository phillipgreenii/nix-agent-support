package claudetranscript

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every test here points the cache at a t.TempDir(): none writes to a real
// cache, home or Claude directory.

func userEvt(uuid, ts, sid, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":%q,"sessionId":%q,"cwd":"/work/gamma","gitBranch":"dev","message":{"role":"user","content":%q}}`,
		uuid, ts, sid, text)
}

func asstEvt(uuid, ts, sid, msgID, model string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":%q,"sessionId":%q,"message":{"id":%q,"model":%q,"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}`,
		uuid, ts, sid, msgID, model, in, out)
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// cacheTree copies the sessions fixture tree into a fresh <tmp>/home/projects
// (the tests mutate it) and adds synthetic transcripts covering every field the
// cache stores: id-less assistant events with and without a uuid, a user turn
// with no uuid, several models, a sidechain message and a file with no dated
// event.
func cacheTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "home", "projects")
	err := filepath.WalkDir(sessionsFixtureDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sessionsFixtureDir, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	writeSessionFile(t, root, "-slug-c", "c1.jsonl",
		userEvt("c1-u1", "2026-09-22T08:00:00Z", "c1", "first"),
		asstEvt("c1-a1", "2026-09-22T08:00:05Z", "c1", "msg_c1", "model-one", 5, 9),
		asstEvt("c1-a2", "2026-09-22T08:00:06Z", "c1", "msg_c1", "model-one", 5, 12),
		asstEvt("c1-a3", "2026-09-22T08:00:07Z", "c1", "", "model-two", 4, 4),
		`{"type":"assistant","timestamp":"2026-09-22T08:00:08Z","sessionId":"c1","message":{"model":"<synthetic>","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"assistant","uuid":"c1-a5","isSidechain":true,"timestamp":"2026-09-22T08:00:09Z","sessionId":"c1","message":{"id":"msg_side","model":"model-three","role":"assistant","content":[],"usage":{"input_tokens":2,"output_tokens":3}}}`,
		`{"type":"user","timestamp":"2026-09-22T08:01:00Z","sessionId":"c1","message":{"role":"user","content":"no uuid here"}}`)
	writeSessionFile(t, root, "-slug-c", "c2.jsonl",
		userEvt("c2-u1", "2026-09-23T09:00:00Z", "c2", "other"),
		asstEvt("c2-a1", "2026-09-23T09:00:05Z", "c2", "msg_c2", "model-two", 8, 1))
	writeSessionFile(t, root, "-slug-c", "undated.jsonl",
		`{"type":"last-prompt","sessionId":"undated"}`)
	return root
}

var cacheWindows = []struct{ since, before time.Time }{
	{},
	{utc(2026, 9, 18, 0, 0, 0), utc(2026, 9, 19, 0, 0, 0)},
	{utc(2026, 9, 20, 0, 0, 0), utc(2026, 9, 21, 0, 0, 0)},
	{utc(2026, 9, 10, 0, 0, 0), utc(2026, 9, 11, 0, 0, 0)},
	{utc(2026, 9, 12, 0, 0, 0), utc(2026, 9, 13, 0, 0, 0)},
	{utc(2026, 9, 21, 0, 0, 0), time.Time{}},
	{time.Time{}, utc(2026, 9, 22, 12, 0, 0)},
}

// assertCachedEqualsUncached compares a cached and an uncached call for every
// window in cacheWindows.
func assertCachedEqualsUncached(t *testing.T, label, root, cache string) {
	t.Helper()
	for _, w := range cacheWindows {
		got, err := SessionsWithOptions(root, w.since, w.before, SessionsOptions{CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		want, err := Sessions(root, w.since, w.before)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s, window [%v, %v): cached result differs from uncached\ncached:   %+v\nuncached: %+v",
				label, w.since, w.before, got, want)
		}
	}
}

// cachedStats runs one open-window cached call and returns its stats.
func cachedStats(t *testing.T, root, cache string, opts SessionsOptions) ([]SessionRecord, SessionsStats) {
	t.Helper()
	var st SessionsStats
	opts.CacheDir, opts.Stats = cache, &st
	recs, err := SessionsWithOptions(root, time.Time{}, time.Time{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return recs, st
}

func loadEntries(t *testing.T, root, cache string) map[string]*cacheEntry {
	t.Helper()
	c := newSummaryCache(root, SessionsOptions{CacheDir: cache})
	if c == nil {
		t.Fatal("no cache for this projects dir")
	}
	m, _ := c.load()
	return m
}

func TestSessionsCache_MatchesUncachedAcrossTreeChanges(t *testing.T) {
	root := cacheTree(t)
	cache := t.TempDir()
	step := func(label string, wantParsed int) {
		t.Helper()
		if _, st := cachedStats(t, root, cache, SessionsOptions{}); st.Parsed != wantParsed {
			t.Errorf("%s: parsed %d files, want %d (%+v)", label, st.Parsed, wantParsed, st)
		}
		assertCachedEqualsUncached(t, label, root, cache)
	}

	step("cold", len(listTranscriptFiles(root)))
	step("warm", 0)

	appendLines(t, filepath.Join(root, "-slug-c", "c1.jsonl"),
		userEvt("c1-u9", "2026-09-22T09:00:00Z", "c1", "later"),
		asstEvt("c1-a9", "2026-09-22T09:00:05Z", "c1", "msg_c9", "model-two", 3, 30))
	step("after appending to a transcript", 1)

	writeSessionFile(t, root, "-slug-d", "d1.jsonl",
		userEvt("d1-u1", "2026-09-24T10:00:00Z", "d1", "new file"))
	step("after adding a file", 1)

	if err := os.Remove(filepath.Join(root, "-slug-c", "c2.jsonl")); err != nil {
		t.Fatal(err)
	}
	step("after deleting a file", 0)
	if _, kept := loadEntries(t, root, cache)["-slug-c/c2.jsonl"]; kept {
		t.Error("the deleted file's entry is still in the cache")
	}

	// A resumed session whose original file is cached, a year old (older than
	// since) and never re-read still resolves through it.
	orig := writeSessionFile(t, root, "-slug-e", "e-orig.jsonl",
		userEvt("e1", "2026-09-10T10:00:00Z", "e-orig", "start"),
		asstEvt("e2", "2026-09-10T10:00:10Z", "e-orig", "msg_e1", "model-one", 7, 11))
	old := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(orig, old, old); err != nil {
		t.Fatal(err)
	}
	step("original file cached", 1)
	writeSessionFile(t, root, "-slug-e", "e-resumed.jsonl",
		userEvt("e1", "2026-09-10T10:00:00Z", "e-res", "start"),
		asstEvt("e2", "2026-09-10T10:00:10Z", "e-res", "msg_e1", "model-one", 7, 11),
		userEvt("e3", "2026-09-12T08:00:00Z", "e-res", "resumed"),
		asstEvt("e4", "2026-09-12T08:00:10Z", "e-res", "msg_e2", "model-one", 1, 2))
	step("after the resume", 1)

	recs, err := SessionsWithOptions(root, utc(2026, 9, 10, 0, 0, 0), utc(2026, 9, 11, 0, 0, 0), SessionsOptions{CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != "e-orig" || recs[0].UserTurns != 2 || recs[0].AssistantTurns != 2 ||
		!recs[0].EndedAt.Equal(utc(2026, 9, 12, 8, 0, 10)) {
		t.Fatalf("resumed session through the cached original = %+v, want one e-orig record, 2 user and 2 assistant turns, ended 2026-09-12T08:00:10Z", recs)
	}
}

func TestSessionsCache_SecondCallOverUnchangedTreeParsesNothing(t *testing.T) {
	root := cacheTree(t)
	cache := t.TempDir()
	first, cold := cachedStats(t, root, cache, SessionsOptions{})
	if cold.Files == 0 || cold.Parsed != cold.Files || cold.Reused != 0 || cold.CacheWrites == 0 || cold.CacheBytes == 0 {
		t.Fatalf("cold stats = %+v, want every file parsed and the cache written", cold)
	}
	second, warm := cachedStats(t, root, cache, SessionsOptions{})
	if warm.Parsed != 0 || warm.Reused != warm.Files || warm.CacheWrites != 0 || warm.CacheBytes != cold.CacheBytes {
		t.Fatalf("warm stats = %+v, want 0 parsed, all %d reused, no write, cache of %d bytes", warm, warm.Files, cold.CacheBytes)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("warm result differs from cold:\n%+v\n%+v", second, first)
	}
	c := newSummaryCache(root, SessionsOptions{CacheDir: cache})
	fi, err := os.Stat(c.path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode %v, want 0600", fi.Mode().Perm())
	}

	// Proof that a warm call reads no transcript: replace every transcript with
	// same-size garbage and restore its mtime. The key is unchanged, so the
	// cached summaries are still served, while an uncached scan finds nothing.
	for _, f := range listTranscriptFiles(root) {
		fi, err := os.Stat(f.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path, bytes.Repeat([]byte("x"), int(fi.Size())), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f.path, fi.ModTime(), fi.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	third, again := cachedStats(t, root, cache, SessionsOptions{})
	if again.Parsed != 0 || !reflect.DeepEqual(third, first) {
		t.Fatalf("after same-key rewrite: parsed %d, records %+v; want 0 parsed and the cached records", again.Parsed, third)
	}
	if plain, _ := Sessions(root, time.Time{}, time.Time{}); plain != nil {
		t.Fatalf("uncached scan of the garbage tree = %+v, want nil (the proof needs the content to matter)", plain)
	}
}

func TestSessionsCache_BadCacheFilesAreIgnoredAndRebuilt(t *testing.T) {
	root := cacheTree(t)
	want, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	reencode := func(t *testing.T, data []byte, version uint64, projects string) []byte {
		t.Helper()
		m, err := decodeCache(data, absRoot)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := encodeCache(&buf, version, projects, m); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	cases := []struct {
		name    string
		mutate  func(t *testing.T, data []byte) []byte
		wantErr error // what decodeCache reports for the mutated file
	}{
		{"garbage", func(*testing.T, []byte) []byte { return []byte("not a cache at all") }, errCacheCorrupt},
		{"empty", func(*testing.T, []byte) []byte { return nil }, errCacheCorrupt},
		{"truncated to half", func(_ *testing.T, d []byte) []byte { return d[:len(d)/2] }, errCacheCorrupt},
		{"truncated by one byte", func(_ *testing.T, d []byte) []byte { return d[:len(d)-1] }, errCacheCorrupt},
		{"one byte flipped", func(_ *testing.T, d []byte) []byte {
			c := bytes.Clone(d)
			c[len(c)/2] ^= 0xff
			return c
		}, errCacheCorrupt},
		{"other format version", func(t *testing.T, d []byte) []byte {
			return reencode(t, d, sessionsCacheVersion+1, absRoot)
		}, errCacheVersion},
		{"other projects dir", func(t *testing.T, d []byte) []byte {
			return reencode(t, d, sessionsCacheVersion, filepath.Join(absRoot, "elsewhere"))
		}, errCacheOtherTree},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := t.TempDir()
			cachedStats(t, root, cache, SessionsOptions{})
			path := newSummaryCache(root, SessionsOptions{CacheDir: cache}).path
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			bad := tc.mutate(t, data)
			if _, err := decodeCache(bad, absRoot); !errors.Is(err, tc.wantErr) {
				t.Fatalf("decodeCache = %v, want %v", err, tc.wantErr)
			}
			if err := os.WriteFile(path, bad, 0o600); err != nil {
				t.Fatal(err)
			}
			got, st := cachedStats(t, root, cache, SessionsOptions{})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("result over a bad cache = %+v, want %+v", got, want)
			}
			if st.Parsed != st.Files || st.CacheWrites == 0 {
				t.Fatalf("stats = %+v, want every file re-read and the cache rebuilt", st)
			}
			if _, st := cachedStats(t, root, cache, SessionsOptions{}); st.Parsed != 0 {
				t.Fatalf("after the rebuild a call parsed %d files, want 0", st.Parsed)
			}
		})
	}

	t.Run("cache path is a directory", func(t *testing.T) {
		cache := t.TempDir()
		path := newSummaryCache(root, SessionsOptions{CacheDir: cache}).path
		if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
			t.Fatal(err)
		}
		got, st := cachedStats(t, root, cache, SessionsOptions{})
		if !reflect.DeepEqual(got, want) || st.CacheWrites != 0 || st.Parsed != st.Files {
			t.Fatalf("got %+v stats %+v, want the uncached result and no write", got, st)
		}
		assertNoTempFiles(t, cache)
	})
}

func assertNoTempFiles(t *testing.T, cache string) {
	t.Helper()
	ents, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestSessionsCache_ConcurrentWritersAreSafe(t *testing.T) {
	root := cacheTree(t)
	cache := t.TempDir()
	want, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	path := newSummaryCache(root, SessionsOptions{CacheDir: cache}).path
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 6 {
				// Half the callers start cold, so writers race each other and the
				// readers on every round (a checkpoint after every file read).
				if (g+i)%2 == 0 {
					_ = os.Remove(path)
				}
				got, err := SessionsWithOptions(root, time.Time{}, time.Time{}, SessionsOptions{CacheDir: cache, checkpointEvery: -1})
				if err != nil || !reflect.DeepEqual(got, want) {
					errs <- fmt.Sprintf("caller %d round %d: err %v, records %+v", g, i, err, got)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	// Every call's last write holds every file, and a removal is always
	// followed by its caller's own write, so the last writer left a complete
	// cache.
	if _, st := cachedStats(t, root, cache, SessionsOptions{}); st.Parsed != 0 {
		t.Errorf("after the race a call parsed %d files (%+v), want a complete cache", st.Parsed, st)
	}
	assertNoTempFiles(t, cache)
}

const (
	checkpointChildRootEnv  = "CLAUDE_TRANSCRIPT_TEST_CHECKPOINT_ROOT"
	checkpointChildCacheEnv = "CLAUDE_TRANSCRIPT_TEST_CHECKPOINT_CACHE"
)

// TestSessionsCacheCheckpointChild is the child half of
// TestSessionsCache_KilledColdCallKeepsCheckpointedFiles, which re-runs this
// test binary with the two env vars set; on its own it skips. The child starts
// a cold call that checkpoints after every file and, once a checkpoint holds
// three files, reports it on stdout and stalls so the parent can SIGKILL it.
func TestSessionsCacheCheckpointChild(t *testing.T) {
	root, cache := os.Getenv(checkpointChildRootEnv), os.Getenv(checkpointChildCacheEnv)
	if root == "" || cache == "" {
		t.Skip("helper process for TestSessionsCache_KilledColdCallKeepsCheckpointedFiles")
	}
	_, _ = SessionsWithOptions(root, time.Time{}, time.Time{}, SessionsOptions{
		CacheDir:        cache,
		checkpointEvery: -1,
		onCheckpoint: func(entries int) {
			if entries >= 3 {
				fmt.Printf("checkpointed %d\n", entries)
				time.Sleep(time.Hour)
			}
		},
	})
	t.Fatal("the cold call finished; the parent should have killed this process first")
}

// A cold call killed part-way (as a caller's deadline does with SIGKILL) leaves
// a complete cache holding the files it checkpointed, and the next call reads
// only the rest.
func TestSessionsCache_KilledColdCallKeepsCheckpointedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home", "projects")
	const nFiles = 12
	for i := range nFiles {
		id := fmt.Sprintf("k%02d", i)
		writeSessionFile(t, root, "-slug-k", id+".jsonl",
			userEvt(id+"-u", fmt.Sprintf("2026-09-%02dT10:00:00Z", i+1), id, "hello"),
			asstEvt(id+"-a", fmt.Sprintf("2026-09-%02dT10:00:05Z", i+1), id, "msg_"+id, "model-one", i, 2*i))
	}
	cache := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionsCacheCheckpointChild$")
	cmd.Env = append(os.Environ(),
		checkpointChildRootEnv+"="+root,
		checkpointChildCacheEnv+"="+cache,
		"GOMAXPROCS=1") // one worker, so the stall stops all reading
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	lines := make(chan string, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "checkpointed ") {
				lines <- sc.Text()
				return
			}
		}
	}()
	var line string
	select {
	case l, ok := <-lines:
		if !ok {
			_ = cmd.Wait()
			t.Fatalf("child exited without a checkpoint; stderr:\n%s", stderr.String())
		}
		line = l
	case <-time.After(2 * time.Minute):
		t.Fatalf("no checkpoint from the child within 2m; stderr:\n%s", stderr.String())
	}
	if err := cmd.Process.Kill(); err != nil { // SIGKILL, as a deadline would
		t.Fatal(err)
	}
	_ = cmd.Wait()

	n, err := strconv.Atoi(strings.TrimPrefix(line, "checkpointed "))
	if err != nil {
		t.Fatal(err)
	}
	if entries := loadEntries(t, root, cache); len(entries) != n || n < 3 || n >= nFiles {
		t.Fatalf("killed call left %d entries (reported %d), want the 3..%d it checkpointed", len(entries), n, nFiles-1)
	}
	got, st := cachedStats(t, root, cache, SessionsOptions{})
	if st.Reused != n || st.Parsed != nFiles-n {
		t.Fatalf("next call stats %+v, want %d reused and %d parsed", st, n, nFiles-n)
	}
	want, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result after the killed call = %+v, want %+v", got, want)
	}
}

func TestSessionsCache_CacheDirInsideClaudeHomeIsNotUsed(t *testing.T) {
	home := filepath.Join(t.TempDir(), "claude-home")
	root := filepath.Join(home, "projects")
	writeSessionFile(t, root, "-s", "a.jsonl", userEvt("a1", "2026-09-01T10:00:00Z", "a", "x"))
	want, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil || len(want) != 1 {
		t.Fatalf("uncached = %+v, %v", want, err)
	}
	for _, dir := range []string{filepath.Join(home, "cache"), filepath.Join(root, "cache"), home} {
		got, st := cachedStats(t, root, dir, SessionsOptions{})
		if !reflect.DeepEqual(got, want) || st.CacheWrites != 0 || st.Reused != 0 {
			t.Errorf("CacheDir %s: got %+v stats %+v, want the uncached result and no cache", dir, got, st)
		}
		if dir != home {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("CacheDir %s inside the Claude home was created (%v)", dir, err)
			}
		}
	}
	// A directory beside the Claude home is fine.
	if _, st := cachedStats(t, root, filepath.Join(filepath.Dir(home), "cache"), SessionsOptions{}); st.CacheWrites != 1 {
		t.Errorf("sibling cache dir: stats %+v, want one write", st)
	}
}

func TestCacheDirAllowed(t *testing.T) {
	cases := []struct {
		projects, dir string
		want          bool
	}{
		{"/u/h/.claude/projects", "/u/h/.cache/pa-monitor", true},
		{"/u/h/.claude/projects", "/u/h", true},
		{"/u/h/.claude/projects", "/u/h/.claudex", true},
		{"/u/h/.claude/projects", "/u/h/.claude", false},
		{"/u/h/.claude/projects", "/u/h/.claude/cache", false},
		{"/u/h/.claude/projects", "/u/h/.claude/projects/x", false},
		{"/projects", "/projects/x", false},
		{"/projects", "/cache", true},
	}
	for _, c := range cases {
		if got := cacheDirAllowed(filepath.FromSlash(c.projects), filepath.FromSlash(c.dir)); got != c.want {
			t.Errorf("cacheDirAllowed(%s, %s) = %v, want %v", c.projects, c.dir, got, c.want)
		}
	}
}

func TestSessionsCache_StaleTempFilesAreRemovedFreshOnesKept(t *testing.T) {
	root := cacheTree(t)
	cache := t.TempDir()
	base := newSummaryCache(root, SessionsOptions{CacheDir: cache}).base
	stale := filepath.Join(cache, base+".tmp-111")
	fresh := filepath.Join(cache, base+".tmp-222")
	unrelated := filepath.Join(cache, "unrelated.tmp-333")
	hourAgo := time.Now().Add(-time.Hour)
	for _, p := range []string{stale, fresh, unrelated} {
		if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{stale, unrelated} {
		if err := os.Chtimes(p, hourAgo, hourAgo); err != nil {
			t.Fatal(err)
		}
	}
	cachedStats(t, root, cache, SessionsOptions{})
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temporary file kept (%v)", err)
	}
	for _, p := range []string{fresh, unrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", filepath.Base(p), err)
		}
	}
}

func TestSessionsCache_UnreadableFileIsNotCached(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any file")
	}
	root := filepath.Join(t.TempDir(), "home", "projects")
	p := writeSessionFile(t, root, "-s", "locked.jsonl", userEvt("z1", "2026-09-01T10:00:00Z", "locked", "x"))
	writeSessionFile(t, root, "-s", "ok.jsonl", userEvt("z2", "2026-09-01T11:00:00Z", "ok", "x"))
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	cache := t.TempDir()
	if recs, st := cachedStats(t, root, cache, SessionsOptions{}); len(recs) != 1 || recs[0].SessionID != "ok" || st.Parsed != 1 {
		t.Fatalf("got %+v stats %+v, want only ok", recs, st)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	recs, st := cachedStats(t, root, cache, SessionsOptions{})
	if len(recs) != 2 || st.Parsed != 1 || st.Reused != 1 {
		t.Fatalf("after chmod: got %+v stats %+v, want both sessions with only the formerly locked file read", recs, st)
	}
}

func TestSessionsCache_MissingOrEmptyTreeTouchesNoCache(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	for _, root := range []string{filepath.Join(t.TempDir(), "absent"), t.TempDir()} {
		recs, st := cachedStats(t, root, cache, SessionsOptions{})
		if recs != nil || st.CacheWrites != 0 {
			t.Fatalf("root %s: got %+v stats %+v", root, recs, st)
		}
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("cache dir created for an empty tree (%v)", err)
	}
}
