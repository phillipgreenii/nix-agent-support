package corpus

import (
	"os"
	"testing"
	"time"
)

func writeStatus(t *testing.T, path, body string, mtime time.Time) (int64, time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size(), fi.ModTime()
}

func TestStatusTail_CacheHitOnUnchanged(t *testing.T) {
	path := t.TempDir() + "/s.status.jsonl"
	size, mtime := writeStatus(t, path, `{"ts":100,"five_hour_pct":50,"five_hour_resets_at":1000}`+"\n", time.Unix(1000, 0))
	st := newStatusTail()
	st.foldFile(path, size, mtime)
	if st.reads != 1 {
		t.Fatalf("reads=%d after first fold, want 1", st.reads)
	}
	st.foldFile(path, size, mtime) // unchanged -> cache hit
	if st.reads != 1 {
		t.Fatalf("reads=%d after cache hit, want 1 (no re-read)", st.reads)
	}
}

func TestStatusTail_ReReadOnSizeChange(t *testing.T) {
	path := t.TempDir() + "/s.status.jsonl"
	size, mtime := writeStatus(t, path, `{"ts":100,"five_hour_pct":50,"five_hour_resets_at":1000}`+"\n", time.Unix(1000, 0))
	st := newStatusTail()
	st.foldFile(path, size, mtime)
	st.foldFile(path, size+10, mtime) // same mtime, larger size (same-mtime append)
	if st.reads != 2 {
		t.Fatalf("reads=%d, want 2 (size change forces re-read)", st.reads)
	}
}

func TestStatusTail_ParsesRecords(t *testing.T) {
	path := t.TempDir() + "/s.status.jsonl"
	body := `{"ts":100,"five_hour_pct":50,"five_hour_resets_at":1000}` + "\n" +
		`{"ts":200,"five_hour_pct":80,"five_hour_resets_at":1000}` + "\n"
	size, mtime := writeStatus(t, path, body, time.Unix(1000, 0))
	recs := newStatusTail().foldFile(path, size, mtime)
	if len(recs) != 2 {
		t.Fatalf("parsed %d records, want 2", len(recs))
	}
}

func TestStatusTail_PruneByPath(t *testing.T) {
	dir := t.TempDir()
	pa := dir + "/a.status.jsonl"
	pb := dir + "/b.status.jsonl"
	sa, ma := writeStatus(t, pa, `{"ts":100,"five_hour_pct":50,"five_hour_resets_at":1000}`+"\n", time.Unix(1000, 0))
	sb, mb := writeStatus(t, pb, `{"ts":100,"five_hour_pct":50,"five_hour_resets_at":1000}`+"\n", time.Unix(1000, 0))
	st := newStatusTail()
	st.foldFile(pa, sa, ma)
	st.foldFile(pb, sb, mb)
	st.prune(map[string]bool{pa: true})
	if _, ok := st.cache[pb]; ok {
		t.Fatalf("prune did not drop b")
	}
	if _, ok := st.cache[pa]; !ok {
		t.Fatalf("prune dropped active a")
	}
}
