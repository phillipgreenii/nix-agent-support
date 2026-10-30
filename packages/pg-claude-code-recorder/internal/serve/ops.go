package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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
	files, err := d.st.AllFiles()
	if err != nil {
		return wire.Coverage{}, err
	}
	gens, err := d.st.AllGenerations()
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
	cov.Roots = make([]wire.RootCoverage, 0, len(d.cfg.Roots()))
	counts := map[string]int{}
	for _, f := range files {
		if f.State == "deleted" {
			cov.Removed++ // a deleted original (the archive path comes in phase 6)
			continue
		}
		paths, _ := d.st.CurrentPaths(f.ID)
		if len(paths) == 0 {
			continue
		}
		p := paths[0]
		cov.Files++
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
		// Phase 1 has no parser: parse lag is reported once a parser exists.
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
		return nil, wire.Errf(wire.ErrInternal, err.Error())
	}
	return cov, nil
}

func (d *Daemon) opStatus(_ context.Context, _ []byte) (any, *wire.Error) {
	cov, err := d.coverage("summary")
	if err != nil {
		return nil, wire.Errf(wire.ErrInternal, err.Error())
	}
	free, _ := FreeBytes(d.cfg.StateDir)
	st := wire.Status{
		CopyLagS:       cov.CopyLagSecondsMax,
		ParseLagS:      0,
		Held:           cov.Held,
		FreeBytes:      free,
		StageCounts:    map[string]int{},
		ParserVersions: map[string]int{},
		SchemaVersion:  d.st.SchemaVersion(),
		ProtocolVer:    wire.ProtocolVersion,
		CaughtUp:       cov.CaughtUp,
		Backfill:       cov.Backfill,
		Socket:         d.cfg.Socket,
	}
	st.Healthy, st.Verdict, st.Short = Verdict(st, d.cfg.MinFreeBytes)
	st.Degraded = !st.CaughtUp || st.CopyLagS >= behindAfter || st.ParseLagS >= behindAfter
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
