package settle

// The incrementally kept stage counts and gauge inputs. transition updates the
// stage counts as a Conversation changes stage; a pass that evaluates a
// Conversation refreshes its held, pending-byte and torn-tail figures from that
// Conversation's own rows. publishGauges sets the stage, held, pending-byte and
// torn-tail gauges from these maps with no store query. The reconciliation
// rebuilds the maps from scratch (refresh) and compares the stage counts it
// finds with the kept ones: a difference is logged once (settle_counts_drift),
// metered, and healed by the rebuild.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

type heldFact struct {
	reason  string
	sinceNS int64
}

// gaugeState holds the figures the gauges are derived from, per Conversation.
// It is guarded by Settler.mu.
type gaugeState struct {
	stage   map[string]string // the stage each Conversation is counted in
	n       map[string]int    // Conversations per stage
	held    map[string]heldFact
	pend    map[string]int64            // pending bytes per Conversation
	pendSum int64                       // pending bytes of the quiet and held ones
	torn    map[string]map[string]int64 // session ("" for a file in none) to source to oldest torn-tail time
}

func newGaugeState() *gaugeState {
	return &gaugeState{
		stage: map[string]string{},
		n:     map[string]int{},
		held:  map[string]heldFact{},
		pend:  map[string]int64{},
		torn:  map[string]map[string]int64{},
	}
}

func pendingStage(stage string) bool { return stage == StageQuiet || stage == StageHeld }

// setStage moves a Conversation to stage ("" uncounts it).
func (g *gaugeState) setStage(session, stage string) {
	old, had := g.stage[session]
	if had {
		g.n[old]--
		if g.n[old] <= 0 {
			delete(g.n, old)
		}
		if pendingStage(old) {
			g.pendSum -= g.pend[session]
		}
		delete(g.stage, session)
	}
	if stage == "" {
		delete(g.held, session)
		return
	}
	g.stage[session] = stage
	g.n[stage]++
	if pendingStage(stage) {
		g.pendSum += g.pend[session]
	}
	if stage != StageHeld {
		delete(g.held, session)
	}
}

// setFacts replaces a Conversation's held, pending-byte and torn-tail figures.
func (g *gaugeState) setFacts(session string, held *heldFact, pend int64, torn map[string]int64) {
	if pendingStage(g.stage[session]) {
		g.pendSum += pend - g.pend[session]
	}
	if pend > 0 {
		g.pend[session] = pend
	} else {
		delete(g.pend, session)
	}
	if held != nil && g.stage[session] == StageHeld {
		g.held[session] = *held
	} else {
		delete(g.held, session)
	}
	if len(torn) > 0 {
		g.torn[session] = torn
	} else {
		delete(g.torn, session)
	}
}

// sessionFacts reads one Conversation's pending bytes (copied but not yet
// archived, summed over the generations of its files) and its torn-tail times by
// source, with queries for that Conversation's files only.
func (s *Settler) sessionFacts(c *conv) (pend int64, torn map[string]int64) {
	for _, f := range c.files {
		gens, err := s.st.Generations(f.ID)
		if err != nil {
			continue
		}
		for _, g := range gens {
			if p := g.CopyOffset - s.st.ArchivedTo(f.ID, g.Gen); p > 0 {
				pend += p
			}
			if t, ok := s.st.TornTailOf(f.ID, g.Gen); ok {
				if torn == nil {
					torn = map[string]int64{}
				}
				if old, ok := torn[f.Source]; !ok || t.AtNS < old {
					torn[f.Source] = t.AtNS
				}
			}
		}
	}
	return pend, torn
}

// noteSession refreshes the kept figures of one Conversation after a pass
// processed it. row is its persisted stage row when it has one.
func (s *Settler) noteSession(c *conv, stage string, row *store.StageRow) {
	var pend int64
	var torn map[string]int64
	pend, torn = s.sessionFacts(c)
	var held *heldFact
	if row != nil && stage == StageHeld {
		held = &heldFact{reason: row.Reason, sinceNS: row.SinceNS}
	}
	s.mu.Lock()
	s.gauges.setFacts(c.session, held, pend, torn)
	s.mu.Unlock()
}

// publishGauges sets the stage, held, pending-byte and torn-tail gauges from the
// kept maps. It issues no store query.
func (s *Settler) publishGauges(now time.Time) {
	reg := s.cfg.Obs.Reg
	s.mu.Lock()
	g := s.gauges
	pub := make(map[string]int, len(gaugeStages))
	for _, st := range gaugeStages {
		pub[st] = g.n[st]
	}
	heldBy := map[string]int{}
	var oldest float64
	for _, h := range g.held {
		heldBy[h.reason]++
		if age := now.Sub(time.Unix(0, h.sinceNS)).Seconds(); age > oldest {
			oldest = age
		}
	}
	pending := g.pendSum
	bySource := map[string]float64{}
	for _, m := range g.torn {
		for source, at := range m {
			if age := now.Sub(time.Unix(0, at)).Seconds(); age > bySource[source] {
				bySource[source] = age
			}
		}
	}
	s.pubCounts, s.pubAt = pub, now
	s.mu.Unlock()
	for _, st := range gaugeStages {
		reg.Set(obs.MConversationsByStage, map[string]string{"stage": st}, float64(pub[st]))
	}
	for _, r := range Reasons {
		reg.Set(obs.MHeldConversations, map[string]string{"reason": r}, float64(heldBy[r]))
	}
	reg.Set(obs.MOldestHeldAge, nil, oldest)
	reg.Set(obs.MBytesPendingSettle, nil, float64(pending))
	for source, age := range bySource {
		reg.Set(obs.MTornTailAge, map[string]string{"source": source}, age)
	}
}

func fmtCounts(m map[string]int) string {
	parts := make([]string, 0, len(gaugeStages))
	for _, st := range gaugeStages {
		parts = append(parts, fmt.Sprintf("%s=%d", st, m[st]))
	}
	return strings.Join(parts, " ")
}

func sameCounts(a, b map[string]int) bool {
	for _, st := range gaugeStages {
		if a[st] != b[st] {
			return false
		}
	}
	return true
}

// keptCounts returns a copy of the kept stage counts.
func (s *Settler) keptCounts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.gauges.n))
	for k, v := range s.gauges.n {
		out[k] = v
	}
	return out
}

// sortedKeys is the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
