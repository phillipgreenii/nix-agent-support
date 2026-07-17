package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// NativePricer is the default CostPricer adapter (ADR 0021 §3): it computes the
// active 5h block cost natively from local transcripts × config prices, with no
// ccusage subprocess. It walks ~/.claude/projects/**/*.jsonl (excluding
// *.status.jsonl sibling files — §2), extracts per-model usage records, windows
// them into 5h blocks, and prices the active block from Prices.
//
// It satisfies poller.CostPricer structurally (ActiveBlock/Probed); the
// compile-time check happens at the composition-root assignment in
// cmd/pa-monitor (importing poller here would cycle, exactly as the retired
// ccusage.Provider noted).
type NativePricer struct {
	ClaudeHome string
	Prices     PriceTable
	// Now returns the current time; injectable for tests. Nil falls back to
	// time.Now.
	Now func() time.Time

	mu      sync.Mutex
	probed  bool
	lastErr error

	// recCache memoizes each transcript's parsed records keyed by path, reused
	// while the file's mtime is unchanged so an unchanged file is never
	// re-opened or re-parsed. Confined to the ActiveBlock caller (the poll-tick
	// goroutine), like the poller's transcriptCache.
	recCache map[string]recEntry
}

// recEntry is one cached file's parsed records and the mtime they were read at.
type recEntry struct {
	mtime   time.Time
	records []Record
}

// ActiveBlock scans transcripts and returns the current active 5h block priced
// per model, or (nil,nil) when there is no active block. Scan/parse errors are
// recorded for Probed() but a partial scan still prices what it read (a single
// unreadable transcript must not blank the whole block), so ActiveBlock itself
// returns a nil error on best-effort success.
func (p *NativePricer) ActiveBlock(_ context.Context) (*Block, error) {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	records, err := p.scanRecordsCached()
	p.mu.Lock()
	p.probed = true
	p.lastErr = err
	p.mu.Unlock()
	return ActiveBlock(records, p.Prices, now()), nil
}

// Probed reports whether a scan has run and the error (if any) from the most
// recent scan — the (probed, lastErr) pair the poller threads onto the tree,
// preserving the ccusage adapter's contract.
func (p *NativePricer) Probed() (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.probed, p.lastErr
}

// scanEvent is the minimal transcript shape the pricer needs.
type scanEvent struct {
	Type              string    `json:"type"`
	Timestamp         time.Time `json:"timestamp"`
	Error             string    `json:"error"`
	IsApiErrorMessage bool      `json:"isApiErrorMessage"`
	Message           struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// scanRecordsCached returns the same record set as scanRecords but reuses the
// parsed records of any transcript whose (path, mtime) are unchanged since the
// last call, so an unchanged file is never re-opened or re-parsed. Records are
// cached only on a clean parse — a file that errored is retried next call so its
// error keeps surfacing (matching scanRecords). Entries for files no longer
// present are pruned. The record set fed to ActiveBlock is byte-for-byte the
// same as scanRecords would produce, so the priced block is identical; only the
// I/O + JSON cost of unchanged files is avoided (bead pg2-l4ssm).
func (p *NativePricer) scanRecordsCached() ([]Record, error) {
	root := filepath.Join(p.ClaudeHome, "projects")
	if p.recCache == nil {
		p.recCache = map[string]recEntry{}
	}
	seen := make(map[string]struct{})
	var records []Record
	var firstErr error

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		if d.IsDir() || !session.IsTranscriptFile(d.Name()) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			if firstErr == nil {
				firstErr = ierr
			}
			return nil
		}
		mt := info.ModTime()
		seen[path] = struct{}{}
		if ent, ok := p.recCache[path]; ok && ent.mtime.Equal(mt) {
			records = append(records, ent.records...)
			return nil
		}
		recs, rerr := scanFile(path)
		if rerr != nil {
			if firstErr == nil {
				firstErr = rerr
			}
			// Do not cache a failed/partial read — retry (and re-surface the
			// error) next call, matching the uncached scanRecords.
			records = append(records, recs...)
			return nil
		}
		p.recCache[path] = recEntry{mtime: mt, records: recs}
		records = append(records, recs...)
		return nil
	})
	if err != nil && firstErr == nil {
		firstErr = err
	}
	// Prune cache entries for files that vanished, so the cache tracks the
	// live corpus rather than growing without bound.
	for path := range p.recCache {
		if _, ok := seen[path]; !ok {
			delete(p.recCache, path)
		}
	}
	return records, firstErr
}

// scanRecords walks claudeHome/projects/**/*.jsonl (transcript files only) and
// returns every priced usage record. Best-effort: unreadable files/lines are
// skipped; the first hard walk error is returned alongside whatever was read.
func scanRecords(claudeHome string) ([]Record, error) {
	root := filepath.Join(claudeHome, "projects")
	var records []Record
	var firstErr error

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A missing projects/ dir (fresh install) is not an error.
			if os.IsNotExist(err) {
				return nil
			}
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		if d.IsDir() || !session.IsTranscriptFile(d.Name()) {
			return nil
		}
		recs, rerr := scanFile(path)
		if rerr != nil && firstErr == nil {
			firstErr = rerr
		}
		records = append(records, recs...)
		return nil
	})
	if err != nil && firstErr == nil {
		firstErr = err
	}
	return records, firstErr
}

// scanFile extracts priced records from one transcript. Error (isApiErrorMessage)
// and zero-usage assistant records are skipped, matching the Snapshot ingestion.
func scanFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var ev scanEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Type != "assistant" || ev.IsApiErrorMessage || ev.Message.Model == "" {
			continue
		}
		u := ev.Message.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 {
			continue
		}
		out = append(out, Record{
			Timestamp: ev.Timestamp,
			Model:     ev.Message.Model,
			Tokens: ModelTokens{
				Input:         u.InputTokens,
				Output:        u.OutputTokens,
				CacheCreation: u.CacheCreationInputTokens,
				CacheRead:     u.CacheReadInputTokens,
			},
		})
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}
