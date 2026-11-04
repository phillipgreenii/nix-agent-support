package settle

// The equivalence test of the event-driven settle loop. Two settlers run over
// two identical stores through one scripted sequence on an injected clock: one
// calls Tick every simulated minute (the full scan, the oracle), the other the
// reconciliation once and then Wake. After every minute the persisted stage rows
// and the stage_changed events must be equal. Wake runs at the minute boundary
// like Tick (the real loop may run earlier; the comparison is made at minute
// granularity, so an early wake is never later than the scan).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func oSess(i int) string { return fmt.Sprintf("0a1b2c3d-0000-4000-8000-%012d", i) }
func oRel(i int) string  { return "projects/example-project/" + oSess(i) + ".jsonl" }

func oLine(i, n int) string {
	return fmt.Sprintf(`{"type":"user","uuid":"u%d","sessionId":%q,"timestamp":"2026-10-29T20:00:%02dZ","cwd":"/example/work","message":{"role":"user","content":"example message %d"}}`+"\n", n, oSess(i), n%60, n)
}

// The mix of states. real Conversations go through the copier (bytes on disk, a
// real parse); the others are committed straight to the store (a settled or
// live Conversation needs no bytes for the stage machine to treat it), which
// keeps a store of 200 affordable in the unit gate.
const (
	cQuietCross = iota // quiet, crossing settle.after during the script
	cQuietOld          // quiet for 48 h: settles in the first pass
	cHeldOffset
	cHeldBehind
	cHeldTail
	cHeldUnparsed
	cHeldTorn
	cHeldRestore
	cSettledReal
	cSettledDirect
	cLive
	cRemovedOld  // old enough to be removed in the first pass
	cRemoveCross // removal deadline crosses during the script
	cQuietRecent
	cCount
)

var oCatName = [cCount]string{"quietCross", "quietOld", "heldOffset", "heldBehind", "heldTail", "heldUnparsed", "heldTorn", "heldRestore", "settledReal", "settledDirect", "live", "removedOld", "removeCross", "quietRecent"}

// oLayout is the number of Conversations of each category: 200 in all, or a
// third of that for the seeded scripts.
func oLayout(small bool) [cCount]int {
	l := [cCount]int{6, 2, 1, 1, 1, 1, 1, 1, 4, 170, 4, 2, 3, 3}
	if small {
		l = [cCount]int{3, 1, 1, 1, 1, 1, 1, 1, 2, 30, 2, 1, 1, 1}
	}
	return l
}

type oracle struct {
	t          *testing.T
	scan, ev   *env
	minute     int
	cat        []int // category of each Conversation
	reconciled bool
}

// group returns the Conversations of a category.
func (o *oracle) group(c int) []int {
	var out []int
	for i, k := range o.cat {
		if k == c {
			out = append(out, i)
		}
	}
	return out
}

func (o *oracle) real(i int) bool {
	switch o.cat[i] {
	case cSettledDirect:
		return false
	}
	return true
}

// newOracle builds the pair of stores, identical but for the settle mode.
func newOracle(t *testing.T, small bool) *oracle {
	mk := func(mode string) *env {
		return newEnvWith(t, false, func(e *env) {
			e.removeOn, e.skipInv = true, true
			e.extra = func(c *Config) { c.Mode, c.MaxLineBytes, c.Recheck = mode, 64, 20*time.Minute }
		})
	}
	o := &oracle{t: t, scan: mk(ModeScan), ev: mk(ModeEvent)}
	for c, n := range oLayout(small) {
		for k := 0; k < n; k++ {
			o.cat = append(o.cat, c)
		}
	}
	o.both(o.buildFixture)
	return o
}

func (o *oracle) both(f func(e *env)) { f(o.scan); f(o.ev) }

func oSeed(e *env, i, lines int, age time.Duration) {
	var s string
	for n := 1; n <= lines; n++ {
		s += oLine(i, n)
	}
	e.write(oRel(i), s, e.clk.Now().Add(-age), false)
	e.ingest(oRel(i))
}

func oFileID(e *env, i int) int64 {
	fl, err := e.st.FilesByCurrentPath(e.path(oRel(i)))
	if err != nil || len(fl) == 0 {
		e.t.Fatalf("no file for %d: %v", i, err)
	}
	return fl[0].ID
}

// directSettled commits a settled Conversation (one file, 10 bytes) without
// copying anything.
func directSettled(e *env, i int) {
	id := e.st.NewFileID()
	m, _ := json.Marshal(store.Manifest{Session: oSess(i), Method: "bytes", Files: []store.ManifestFile{{File: id, Mode: "append", Gen: 1, Size: 10}}})
	val, _ := json.Marshal(store.SettleRec{Manifest: m})
	at := e.clk.Now().Add(-48 * time.Hour).UnixNano()
	if err := e.st.Commit(
		&store.Rec{T: store.TFile, At: at, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: uint64(id), Birth: 3},
		&store.Rec{T: store.TPath, At: at, File: id, Path: e.path(oRel(i))},
		&store.Rec{T: store.TGen, At: at, File: id, Gen: 1, Method: "bytes", Reason: "first"},
		&store.Rec{T: store.TCommit, At: at, File: id, Gen: 1, From: 0, To: 10, Size: 10, Tail: 1, TailLen: 1},
		&store.Rec{T: store.TSettle, At: at, Session: oSess(i), Method: "bytes", Val: string(val)},
		&store.Rec{T: store.TStage, At: at, Session: oSess(i), State: store.StageSettled},
	); err != nil {
		e.t.Fatal(err)
	}
}

func (o *oracle) buildFixture(e *env) {
	old := 48 * time.Hour
	seen := map[int]int{}
	for i, c := range o.cat {
		k := seen[c]
		seen[c]++
		switch c {
		case cQuietCross: // settle.after passes between minute 0 and about 35
			oSeed(e, i, 3, 24*time.Hour-time.Duration(k*7)*time.Minute)
		case cQuietOld, cSettledReal:
			oSeed(e, i, 3, old)
		case cHeldOffset: // the original grew, not ingested
			oSeed(e, i, 3, old)
			e.write(oRel(i), oLine(i, 9), e.clk.Now().Add(-old), true)
		case cHeldBehind: // copied, not parsed
			oSeed(e, i, 3, old)
			e.write(oRel(i), oLine(i, 9), e.clk.Now().Add(-old), true)
			if _, err := e.cp.Ingest(e.path(oRel(i))); err != nil {
				e.t.Fatal(err)
			}
		case cHeldTail: // same size, different tail
			oSeed(e, i, 3, old)
			p := e.path(oRel(i))
			b, _ := os.ReadFile(p)
			b[len(b)-3] ^= 0x01
			_ = os.WriteFile(p, b, 0o644)
			_ = os.Chtimes(p, e.clk.Now().Add(-old), e.clk.Now().Add(-old))
		case cHeldUnparsed:
			oSeed(e, i, 3, old)
			id := oFileID(e, i)
			if err := e.st.WithWrite("", func(tx *sql.Tx) error { _, err := tx.Exec(`DELETE FROM parse_state WHERE file_id = ?`, id); return err }); err != nil {
				e.t.Fatal(err)
			}
		case cHeldTorn: // a torn tail past maxLineBytes (64 here)
			oSeed(e, i, 3, old)
			e.write(oRel(i), strings.Repeat("x", 200), e.clk.Now().Add(-old), true)
			if _, err := e.cp.Ingest(e.path(oRel(i))); err != nil {
				e.t.Fatal(err)
			}
		case cHeldRestore: // a held row carrying the restore_conflict reason
			oSeed(e, i, 3, old)
			if err := e.st.Commit(&store.Rec{T: store.TStage, At: e.clk.Now().UnixNano(), Session: oSess(i), State: StageHeld, Reason: ReasonRestoreConflict}); err != nil {
				e.t.Fatal(err)
			}
		case cSettledDirect:
			directSettled(e, i)
		case cLive:
			oSeed(e, i, 3, old)
			e.live[oSess(i)] = true
		case cRemovedOld:
			oSeed(e, i, 3, 31*24*time.Hour)
		case cRemoveCross: // the removal deadline passes between minute 10 and 30
			oSeed(e, i, 3, 30*24*time.Hour-time.Duration((k+1)*10)*time.Minute)
		case cQuietRecent:
			oSeed(e, i, 3, time.Hour)
		}
	}
}

type oAct struct {
	kind string
	i    int
}

func (o *oracle) apply(a oAct) {
	switch a.kind {
	case "append":
		o.both(func(e *env) {
			e.write(oRel(a.i), oLine(a.i, 20+o.minute), e.clk.Now(), true)
			e.ingest(oRel(a.i))
		})
	case "touch", "touchAge": // mtime only: no copier commit, no dirty signal
		o.both(func(e *env) {
			at := e.clk.Now()
			if a.kind == "touchAge" { // a later quiet deadline (minute 13 for a touch at 5) that falls before the low-space window
				at = at.Add(-24*time.Hour + 8*time.Minute)
			}
			if _, err := os.Stat(e.path(oRel(a.i))); err == nil {
				_ = os.Chtimes(e.path(oRel(a.i)), at, at)
			}
		})
	case "liveOn", "liveOff":
		on := a.kind == "liveOn"
		o.both(func(e *env) { e.live[oSess(a.i)] = on })
		if a.i%2 == 0 {
			o.ev.sett.LiveChanged() // the tracker's callback; odd ones are left to the poll
		}
	case "lowOn", "lowOff":
		o.both(func(e *env) {
			if a.kind == "lowOn" {
				e.free = 0
			} else {
				e.free = 1 << 40
			}
		})
	case "parse": // the parser catches up (no copier commit, so no dirty signal)
		o.both(func(e *env) { e.prs.Notify(oFileID(e, a.i)) })
	case "fixheld": // the copier catches up with a grown original
		o.both(func(e *env) { e.ingest(oRel(a.i)) })
	case "restart": // a restart forgets when each held Conversation was checked
		o.both(func(e *env) {
			e.sett.mu.Lock()
			e.sett.checkedAt = map[string]time.Time{}
			e.sett.mu.Unlock()
		})
		o.reconciled = true // the event settler's pass this minute is the start-up reconciliation
	}
}

func stageDump(e *env) []string {
	rows, err := e.st.Stages()
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s %s %s %d", r.Session, r.Stage, r.Reason, r.SinceNS))
	}
	sort.Strings(out)
	return out
}

func eventDump(e *env) []string {
	var out []string
	for _, ev := range e.obs.EventsNamed(obs.EvStageChanged) {
		a := ev.Attrs
		out = append(out, fmt.Sprintf("%s %s>%s %s", a["session"], a["from"], a["to"], a["reason"]))
	}
	sort.Strings(out)
	return out
}

func (o *oracle) compare() {
	o.t.Helper()
	diff := func(what string, a, b []string) {
		if strings.Join(a, "\n") == strings.Join(b, "\n") {
			return
		}
		seen := map[string]int{}
		for _, x := range a {
			seen[x]++
		}
		for _, x := range b {
			seen[x]--
		}
		var d []string
		for k, v := range seen {
			if v != 0 {
				d = append(d, fmt.Sprintf("%+d %s", v, k))
			}
		}
		sort.Strings(d)
		if len(d) > 12 {
			d = d[:12]
		}
		o.t.Fatalf("minute %d: %s differ (+ scan only, - event only):\n%s", o.minute, what, strings.Join(d, "\n"))
	}
	diff("stage rows", stageDump(o.scan), stageDump(o.ev))
	diff("stage_changed events", eventDump(o.scan), eventDump(o.ev))
}

// run plays the script: acts maps a minute to its actions, applied in order.
func (o *oracle) run(minutes int, acts map[int][]oAct) {
	o.t.Helper()
	// Minute 0 is the start: the scan settler's first Tick, the event settler's
	// start-up reconciliation.
	o.scan.tick()
	o.ev.tick()
	o.compare()
	for o.minute = 1; o.minute <= minutes; o.minute++ {
		o.both(func(e *env) { e.clk.Advance(time.Minute) })
		for _, a := range acts[o.minute] {
			o.apply(a)
		}
		o.scan.tick()
		if o.reconciled {
			o.ev.tick()
			o.reconciled = false
		} else if _, err := o.ev.sett.Wake(o.ev.clk.Now()); err != nil {
			o.t.Fatal(err)
		}
		o.compare()
	}
}

func oracleScript(o *oracle) map[int][]oAct {
	each := func(kind string, ids ...int) []oAct {
		var out []oAct
		for _, i := range ids {
			out = append(out, oAct{kind, i})
		}
		return out
	}
	head := func(ids []int, n int) []int {
		if n > len(ids) {
			n = len(ids)
		}
		return ids[:n]
	}
	cat := func(parts ...[]oAct) []oAct {
		var out []oAct
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	quiet, recent := o.group(cQuietCross), o.group(cQuietRecent)
	live, settled := o.group(cLive), o.group(cSettledReal)
	return map[int][]oAct{
		// settled then appended; an mtime-only change on quiet Conversations
		5: cat(each("append", head(o.group(cSettledReal)[3:], 2)...), each("touchAge", head(quiet[1:], 1)...), each("touch", head(quiet[3:], 2)...), each("touch", head(recent, 2)...)),
		// process exits, and settled Conversations gaining a process
		10: cat(each("liveOff", head(live, 5)...), each("liveOn", head(settled, 3)...)),
		15: {{"lowOn", 0}},
		// held.recheck is 20 minutes here: the unparsed file is released by the
		// recheck deadline alone, the grown original by a dirty signal.
		12: each("parse", o.group(cHeldUnparsed)...),
		25: each("fixheld", o.group(cHeldOffset)...),
		30: {{"lowOff", 0}},
		40: each("liveOff", head(settled, 3)...),
		// removed Conversations whose path reappears
		35: each("append", head(o.group(cRemovedOld), 2)...),
		38: {{"restart", 0}},
		42: each("append", head(recent, 2)...),
		44: each("touch", head(recent[2:], 1)...),
	}
}

func TestEventLoopMatchesTheFullScan(t *testing.T) {
	o := newOracle(t, false)
	if len(o.cat) != 200 {
		t.Fatalf("fixture has %d Conversations, want 200", len(o.cat))
	}
	o.run(46, oracleScript(o))
}

func (o *oracle) randomScript(seed int64, minutes int) map[int][]oAct {
	rng := rand.New(rand.NewSource(seed))
	acts := map[int][]oAct{}
	live := map[int]bool{}
	low := false
	var reals, touchable, notRemoved []int
	for i := range o.cat {
		if o.real(i) {
			reals = append(reals, i)
		}
		if c := o.cat[i]; c == cQuietCross || c == cQuietRecent {
			touchable = append(touchable, i)
		}
		if c := o.cat[i]; o.real(i) && c != cRemovedOld && c != cRemoveCross {
			notRemoved = append(notRemoved, i)
		}
	}
	pick := func(from []int) int { return from[rng.Intn(len(from))] }
	for m := 1; m <= minutes; m++ {
		for n := rng.Intn(3); n > 0; n-- {
			switch rng.Intn(7) {
			case 0, 1:
				acts[m] = append(acts[m], oAct{"append", pick(reals)})
			case 2:
				acts[m] = append(acts[m], oAct{"touch", pick(touchable)})
			case 3:
				if i := pick(notRemoved); !live[i] {
					live[i] = true
					acts[m] = append(acts[m], oAct{"liveOn", i})
				}
			case 4:
				for j := range live {
					if live[j] {
						live[j] = false
						acts[m] = append(acts[m], oAct{"liveOff", j})
						break
					}
				}
			case 5:
				low = !low
				kind := "lowOff"
				if low {
					kind = "lowOn"
				}
				acts[m] = append(acts[m], oAct{kind, 0})
			case 6:
				if rng.Intn(4) == 0 {
					acts[m] = append(acts[m], oAct{"restart", 0})
				}
			}
		}
	}
	return acts
}

// oracleSeeds is the fixed seed list; the property test is reproducible.
var oracleSeeds = []int64{3, 17}

func TestEventLoopMatchesTheFullScanOnSeededScripts(t *testing.T) {
	for _, seed := range oracleSeeds {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			o := newOracle(t, true)
			o.run(45, o.randomScript(seed, 45))
		})
	}
}
