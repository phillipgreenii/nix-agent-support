//go:build crashsweep

package settle

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/invariants"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// A model-based sequence test: rapid draws a random sequence of the operations a
// running recorder sees (transcript appends and rotations, clock advances,
// Tick, Wake, Dirty, LiveChanged, Remove, Extract, Doctor, a crash and a
// reopen), runs it on a real settler and store over a real temporary directory
// (the fake clock, clone policy and process listing of env_test.go), and checks
// invariants.Violations after EVERY step. A failing sequence is shrunk by rapid
// to a minimal one. Run more checks with `go test -run TestModelSequence
// -rapid.checks=2000`; replay a failure with the -rapid.failfile it prints.

// modelSessions are the two Conversations the model drives. Two, not one: the
// invariants and Remove/Extract bookkeeping have cross-Conversation paths (the
// removal caps, a held Conversation next to a settled one) that one cannot reach;
// more only slow each step.
var modelSessions = []string{sess, "0a1b2c3d-0000-4000-8000-000000000002"}

// modelAdvances are the clock steps. Each crosses a different threshold of the
// configuration in newEnv: 1 minute stays inside every quiet window, 2 hours
// passes the 1 hour recheck, 25 hours passes the 24 hour settle delay, and 31
// days passes the 30 day removal age (removeAge).
var modelAdvances = []time.Duration{time.Minute, 2 * time.Hour, 25 * time.Hour, 31 * 24 * time.Hour}

type modelRun struct {
	t *rapid.T
	e *env
	// content is the bytes the model believes each Claude transcript holds.
	content map[string]string
	// lines numbers the lines written, so every line is unique.
	lines int
	// removed holds the transcript bytes at the moment the Conversation was seen
	// removed; an Extract MUST give them back.
	removed map[string]string
	step    int
}

func (m *modelRun) rel(session string) string {
	return "projects/example-project/" + session + ".jsonl"
}

func (m *modelRun) stage(session string) string {
	r, ok, err := m.e.st.Stage(session)
	if err != nil {
		m.t.Fatalf("stage: %v", err)
	}
	if !ok {
		return StageQuiet
	}
	return r.Stage
}

// check runs after every step: the structural invariants, and the model's own
// bookkeeping of removed Conversations.
func (m *modelRun) check(what string) {
	m.step++
	if v := invariants.Violations(m.e.st, invariants.Options{
		ClaudeDir: m.e.claude, AllowClaudeChange: true, ArchiveCompare: invariants.RecordedBytes(m.e.st),
	}); len(v) != 0 {
		m.t.Fatalf("step %d (%s): invariants violated: %v", m.step, what, v)
	}
	for _, s := range modelSessions {
		if m.stage(s) == store.StageRemoved {
			if _, seen := m.removed[s]; !seen {
				m.removed[s] = m.content[s]
			}
		} else {
			delete(m.removed, s)
		}
	}
}

// ingest is env.ingest without t.Fatal, so rapid sees the failure and shrinks it.
func (m *modelRun) ingest(rel string) {
	res, err := m.e.cp.Ingest(m.e.path(rel))
	if err != nil {
		m.t.Fatalf("ingest %s: %v", rel, err)
	}
	m.e.prs.Notify(res.File)
	if err := m.e.prs.Drain(); err != nil {
		m.t.Fatalf("drain: %v", err)
	}
}

func (m *modelRun) writable(session string) bool {
	return m.stage(session) != store.StageRemoved && m.stage(session) != store.StageExtracting
}

func (m *modelRun) newLine() string {
	m.lines++
	return line(m.lines)
}

// crash is env.crash without t.Fatal.
func (m *modelRun) crash() {
	_ = m.e.st.Close()
	m.e.open()
	if err := m.e.cp.Recover(); err != nil {
		m.t.Fatalf("copier recover: %v", err)
	}
	if err := m.e.sett.Recover(); err != nil {
		m.t.Fatalf("settler recover: %v", err)
	}
}

func TestModelSequence(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Event mode, so Wake is a real pass; remove.enable on, so Remove and the
		// automatic removal pass run. The env's own end-of-test check is replaced by
		// the per-step check, and the store is closed at the end of each run: rapid
		// runs the body many times against the one *testing.T.
		e := newEnvWith(t, rapid.Bool().Draw(rt, "clone"), func(e *env) {
			e.removeOn = true
			e.skipInv = true
			e.extra = func(c *Config) { c.Mode = ModeEvent }
		})
		defer func() { _ = e.st.Close() }()
		m := &modelRun{t: rt, e: e, content: map[string]string{}, removed: map[string]string{}}

		pick := rapid.SampledFrom(modelSessions)
		rt.Repeat(map[string]func(*rapid.T){
			"append": func(rt *rapid.T) {
				s := pick.Draw(rt, "session")
				if !m.writable(s) {
					rt.Skip("the Conversation is removed")
				}
				// 1 to 3 lines: enough to grow a copy by several records, small enough
				// that a rotation (below) is usually a different length.
				var add string
				for i := rapid.IntRange(1, 3).Draw(rt, "lines"); i > 0; i-- {
					add += m.newLine()
				}
				e.write(m.rel(s), add, e.clk.Now(), true)
				m.content[s] += add
				m.ingest(m.rel(s))
				m.check("append " + s)
			},
			"rotate": func(rt *rapid.T) {
				s := pick.Draw(rt, "session")
				if !m.writable(s) || m.content[s] == "" {
					rt.Skip("nothing to rotate")
				}
				// Claude starts a fresh transcript under the same name: the old bytes
				// are gone from the original, the recorder must keep them.
				fresh := m.newLine()
				e.write(m.rel(s), fresh, e.clk.Now(), false)
				m.content[s] = fresh
				m.ingest(m.rel(s))
				m.check("rotate " + s)
			},
			"advance": func(rt *rapid.T) {
				d := rapid.SampledFrom(modelAdvances).Draw(rt, "by")
				e.clk.Advance(d)
				m.check(fmt.Sprintf("advance %s", d))
			},
			"tick": func(rt *rapid.T) {
				if _, err := e.sett.Tick(); err != nil {
					rt.Fatalf("tick: %v", err)
				}
				m.check("tick")
			},
			"wake": func(rt *rapid.T) {
				if _, err := e.sett.Wake(e.clk.Now()); err != nil {
					rt.Fatalf("wake: %v", err)
				}
				m.check("wake")
			},
			"dirty": func(rt *rapid.T) {
				s := pick.Draw(rt, "session")
				var id int64 = 9999 // a file the settler has not placed
				if fl, err := e.st.FilesByCurrentPath(e.path(m.rel(s))); err == nil && len(fl) > 0 && rapid.Bool().Draw(rt, "known") {
					id = fl[0].ID
				}
				e.sett.Dirty(id, int64(rapid.IntRange(0, len(m.content[s])).Draw(rt, "offset")))
				m.check("dirty")
			},
			"live-changed": func(rt *rapid.T) {
				s := pick.Draw(rt, "session")
				e.live[s] = rapid.Bool().Draw(rt, "live")
				e.sett.LiveChanged()
				m.check("live-changed")
			},
			"remove": func(rt *rapid.T) {
				opt := RemoveOptions{}
				if rapid.Bool().Draw(rt, "one") {
					opt.Sessions = []string{pick.Draw(rt, "session")}
				}
				if _, err := e.sett.Remove(opt); err != nil {
					rt.Fatalf("remove: %v", err)
				}
				m.check("remove")
			},
			"extract": func(rt *rapid.T) {
				s := pick.Draw(rt, "session")
				_, err := e.sett.Extract(s, "")
				// A Conversation that is not removed, a live one and one whose file
				// is back are refused by design.
				if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, ErrHeldByLive) && !errors.Is(err, ErrTargetExists) {
					rt.Fatalf("extract %s: %v", s, err)
				}
				if err == nil {
					want, ok := m.removed[s]
					got, rerr := os.ReadFile(e.path(m.rel(s)))
					if rerr != nil {
						rt.Fatalf("extract %s: %v", s, rerr)
					}
					if ok && string(got) != want {
						rt.Fatalf("extract %s: bytes differ from what was removed (%d bytes, want %d)", s, len(got), len(want))
					}
					m.content[s] = string(got)
				}
				m.check("extract " + s)
			},
			"doctor": func(rt *rapid.T) {
				if _, err := e.sett.Doctor(); err != nil {
					rt.Fatalf("doctor: %v", err)
				}
				m.check("doctor")
			},
			"crash": func(rt *rapid.T) {
				m.crash()
				m.check("crash and reopen")
			},
		})
	})
}
