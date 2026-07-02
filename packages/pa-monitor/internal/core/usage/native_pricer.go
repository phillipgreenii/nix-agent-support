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
	records, err := scanRecords(p.ClaudeHome)
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
	defer f.Close()

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
