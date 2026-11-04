package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// behindAfter is the copy and parse lag, in seconds, that makes the verdict
// "behind" (healthy means both lags are under 60 seconds).
const behindAfter = 60.0

type genKey struct {
	file int64
	gen  int
}

// coverage reads every file's copy and parse state against the original on
// disk. A file that has no current path (every path closed) is not listed.
func (d *Daemon) coverage(view string) (wire.Coverage, error) {
	return d.computeCoverage(view, true)
}

// computeCoverage is coverage; withSummary false skips the bad-line and
// unknown-key aggregates, which the status figures do not read.
func (d *Daemon) computeCoverage(view string, withSummary bool) (wire.Coverage, error) {
	// One read transaction for every statement: one lock round trip instead of
	// one per statement, and the figures come from one snapshot.
	var cov wire.Coverage
	err := d.st.ReadTx(func(rd *store.Reader) error {
		var e error
		cov, e = d.computeCoverageFrom(rd, view, withSummary)
		return e
	})
	return cov, err
}

func (d *Daemon) computeCoverageFrom(rd *store.Reader, view string, withSummary bool) (wire.Coverage, error) {
	files, err := rd.AllFiles()
	if err != nil {
		return wire.Coverage{}, err
	}
	gens, err := rd.AllGenerations()
	if err != nil {
		return wire.Coverage{}, err
	}
	curPaths, err := rd.AllCurrentPaths() // one query for every file, not one per file
	if err != nil {
		return wire.Coverage{}, err
	}
	byGen := make(map[genKey]int64, len(gens)) // copy offset
	parse := make(map[genKey]int64, len(gens))
	for _, g := range gens {
		byGen[genKey{g.File, g.Gen}] = g.CopyOffset
		parse[genKey{g.File, g.Gen}] = g.ParseOffset
	}
	now := d.clock.Now()
	var cov wire.Coverage
	kinds := map[string]int{}
	cov.Roots = make([]wire.RootCoverage, 0, len(d.cfg.Roots()))
	counts := map[string]int{}
	for _, f := range files {
		if f.State == "deleted" {
			cov.Removed++ // a deleted original (the archive path comes in phase 6)
			continue
		}
		paths := curPaths[f.ID]
		if len(paths) == 0 {
			continue
		}
		p := paths[0]
		cov.Files++
		if !d.prs.HasSource(f.Source) {
			kinds[f.Source]++
		}
		for _, root := range d.cfg.Roots() {
			if strings.HasPrefix(p, root+string(os.PathSeparator)) {
				counts[root]++
				break
			}
		}
		fc := wire.FileCoverage{FileID: f.ID, Source: f.Source, Path: p, State: f.State}
		if fi, err := os.Lstat(p); err == nil {
			fc.Size = fi.Size()
			if f.Mode == "append" && f.CurGen > 0 {
				k := genKey{f.ID, f.CurGen}
				fc.CopyOffset = byGen[k]
				fc.ParseOffset = parse[k]
				if lag := fc.Size - fc.CopyOffset; lag > 0 {
					fc.CopyLagBytes = lag
					if s := now.Sub(fi.ModTime()).Seconds(); s > 0 {
						fc.CopyLagS = s
					}
				}
			} else {
				fc.CopyOffset = fc.Size // a snapshot file is copied whole, version by version
			}
		}
		if f.Mode == "append" && d.prs.HasSource(f.Source) && fc.CopyOffset > fc.ParseOffset {
			fc.ParseLagBytes = fc.CopyOffset - fc.ParseOffset
			if fc.ParseLagBytes > cov.ParseLagBytesMax {
				cov.ParseLagBytesMax = fc.ParseLagBytes
			}
			if s := parseLagSeconds(rd, f.ID, f.CurGen, fc.ParseOffset, now); s > cov.ParseLagSecondsMax {
				cov.ParseLagSecondsMax = s
			}
		}
		if fc.CopyLagBytes > cov.CopyLagBytesMax {
			cov.CopyLagBytesMax = fc.CopyLagBytes
			cov.Worst = &wire.Worst{FileID: f.ID, Path: p, CopyLagBytes: fc.CopyLagBytes}
		}
		if fc.CopyLagS > cov.CopyLagSecondsMax {
			cov.CopyLagSecondsMax = fc.CopyLagS
		}
		if view == "files" {
			cov.FileList = append(cov.FileList, fc)
		}
	}
	for _, root := range d.cfg.Roots() {
		_, err := os.Stat(root)
		cov.Roots = append(cov.Roots, wire.RootCoverage{Path: root, Present: err == nil, Files: counts[root]})
	}
	if !withSummary {
		// the caller does not read the aggregates
	} else if sum, err := parser.SummarizeFrom(rd); err == nil {
		cov.BadLines = sum.BadLines
		cov.UnknownKeys = []wire.UnknownKey{}
		for _, u := range sum.UnknownKeys {
			cov.UnknownKeys = append(cov.UnknownKeys, wire.UnknownKey{Source: u.Source, Type: u.Type, Path: u.Path, Count: u.Count})
		}
	}
	cov.UnparsedKinds = []wire.UnparsedKind{}
	for k, n := range kinds {
		cov.UnparsedKinds = append(cov.UnparsedKinds, wire.UnparsedKind{Kind: k, Files: n})
	}
	sort.Slice(cov.UnparsedKinds, func(i, j int) bool { return cov.UnparsedKinds[i].Kind < cov.UnparsedKinds[j].Kind })
	if rows, err := rd.Stages(); err == nil {
		for _, r := range rows {
			switch r.Stage {
			case store.StageHeld:
				cov.Held++
			case store.StageSettled:
				cov.Settled++
			case store.StageRemoved:
				cov.RemovedConversations++
			}
		}
	}
	cov.CaughtUp = d.backfillDone.Load()
	cov.Degraded = !cov.CaughtUp
	cov.Backfill = d.backfillProgress()
	return cov, nil
}

func (d *Daemon) backfillProgress() *wire.Backfill {
	b := &wire.Backfill{Copied: int(d.filesCopied.Load()), Total: int(d.filesTotal.Load()), Running: !d.backfillDone.Load()}
	if b.Running && b.Copied > 0 && b.Total > b.Copied {
		if at := d.backfillAt.Load(); at > 0 {
			elapsed := d.clock.Now().Sub(time.Unix(0, at)).Seconds()
			b.EtaS = elapsed / float64(b.Copied) * float64(b.Total-b.Copied)
		}
	}
	return b
}

func (d *Daemon) opCoverage(_ context.Context, body []byte) (any, *wire.Error) {
	var req wire.CoverageRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, wire.Errf(wire.ErrBadRequest, "coverage request: "+err.Error())
		}
	}
	switch req.View {
	case "", "summary", "files":
	default:
		return nil, wire.Errf(wire.ErrBadRequest, fmt.Sprintf("unknown coverage view %q (summary or files)", req.View))
	}
	cov, err := d.coverage(req.View)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	return cov, nil
}

// opStatus answers from the snapshot the status loop keeps: it issues no
// query and touches no file, so its cost does not grow with the file count.
func (d *Daemon) opStatus(_ context.Context, _ []byte) (any, *wire.Error) {
	snap, err := d.statusSnapshot()
	if err != nil {
		return nil, wire.FromErr(err)
	}
	free, _ := FreeBytes(d.cfg.StateDir)
	now := d.clock.Now()
	heldLong := 0
	for _, since := range snap.heldSince {
		if now.Sub(time.Unix(0, since)) > d.cfg.HeldMaxAge {
			heldLong++
		}
	}
	counts := make(map[string]int, len(snap.stageCounts))
	for k, v := range snap.stageCounts {
		counts[k] = v
	}
	caughtUp := d.backfillDone.Load()
	st := wire.Status{
		CopyLagS:       snap.cov.CopyLagSecondsMax,
		ParseLagS:      snap.cov.ParseLagSecondsMax,
		Held:           heldLong,
		FreeBytes:      free,
		StageCounts:    counts,
		ParserVersions: map[string]int{},
		SchemaVersion:  d.st.SchemaVersion(),
		ProtocolVer:    wire.ProtocolVersion,
		CaughtUp:       caughtUp,
		Backfill:       d.backfillProgress(),
		Socket:         d.cfg.Socket,
	}
	if !snap.at.IsZero() {
		st.StatusAgeS = now.Sub(snap.at).Seconds()
	}
	st.Healthy, st.Verdict, st.Short = Verdict(st, d.cfg.MinFreeBytes)
	st.Degraded = !st.CaughtUp || st.CopyLagS >= behindAfter || st.ParseLagS >= behindAfter
	if snap.warming {
		st.Healthy, st.Short, st.Degraded = false, "behind", true
		st.Verdict = "behind: the status figures are still being computed"
	}
	return st, nil
}

// Verdict derives healthy, the one-sentence verdict and the short word from the
// measured fields: healthy means copy and parse lag under 60 seconds, nothing
// held too long and free space above minFree.
func Verdict(st wire.Status, minFree int64) (healthy bool, verdict, short string) {
	switch {
	case !st.CaughtUp:
		b := st.Backfill
		if b != nil && b.Total > 0 {
			return false, fmt.Sprintf("behind: first backfill is copying, %d of %d files copied (newest first)", b.Copied, b.Total), "behind"
		}
		return false, "behind: first backfill has not finished its first sweep", "behind"
	case st.CopyLagS >= behindAfter || st.ParseLagS >= behindAfter:
		return false, fmt.Sprintf("behind: copy lag %.0fs and parse lag %.0fs (healthy is under %.0fs)", st.CopyLagS, st.ParseLagS, behindAfter), "behind"
	case st.Held > 0:
		return false, fmt.Sprintf("held: %d Conversation(s) held", st.Held), "held"
	case st.FreeBytes < minFree:
		return false, fmt.Sprintf("behind: %d bytes free is below minFreeBytes %d", st.FreeBytes, minFree), "behind"
	}
	return true, fmt.Sprintf("healthy: copy lag %.0fs, parse lag %.0fs, nothing held, %d bytes free", st.CopyLagS, st.ParseLagS, st.FreeBytes), "ok"
}

// parseLagSeconds is how long the byte at the parse offset has waited: the time
// since the recorder saw it.
func parseLagSeconds(rd *store.Reader, file int64, gen int, off int64, now time.Time) float64 {
	cc, err := rd.CopyCommits(file)
	if err != nil {
		return 0
	}
	for i := len(cc) - 1; i >= 0; i-- {
		if c := cc[i]; c.Gen == gen && c.From <= off && off < c.To {
			if s := now.Sub(time.Unix(0, c.SeenNS)).Seconds(); s > 0 {
				return s
			}
		}
	}
	return 0
}

func (d *Daemon) opQuery(ctx context.Context, body []byte) (any, *wire.Error) {
	var req query.Request
	if err := query.Decode(body, &req); err != nil {
		return nil, err
	}
	ans, err := d.qe.Query(ctx, req)
	if err != nil {
		return nil, err
	}
	return ans, nil
}

func (d *Daemon) opSearch(ctx context.Context, body []byte) (any, *wire.Error) {
	var req query.SearchRequest
	if err := query.Decode(body, &req); err != nil {
		return nil, err
	}
	ans, err := d.qe.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	return ans, nil
}
