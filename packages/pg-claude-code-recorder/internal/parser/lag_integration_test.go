//go:build integration

package parser_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func TestLagEquivalenceRandomSteps(t *testing.T) {
	for _, seed := range []int64{1, 2, 3} {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			k := newLagKit(t)
			l := startLag(t, k)
			rng := rand.New(rand.NewSource(seed))
			// 300 append files over 3 sources: transcripts, history, and a
			// source without a parser.
			var ids []int64
			torn := map[int64]string{}
			n := 0
			line := func() string { n++; return fmt.Sprintf(lagLine, n, n) }
			for i := 0; i < 300; i++ {
				switch i % 3 {
				case 0:
					ids = append(ids, k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", i), line()))
				case 1:
					ids = append(ids, k.Add("example-proj", "history.jsonl", `{"display":"one","timestamp":1761768000000}`+"\n"))
				default:
					ids = append(ids, addNoParser(t, k, 1_700_000_000_000_000_000+int64(i)*10, 100+int64(i)))
				}
			}
			lagEqual(t, k, l, "added")
			deleted := 0
			for step := 0; step < 60; step++ {
				id := ids[rng.Intn(len(ids))]
				label := fmt.Sprintf("step %d", step)
				switch op := rng.Intn(10); {
				case op < 4: // append: whole lines, or a torn first half then its rest
					if rest, ok := torn[id]; ok {
						k.Append(id, rest)
						delete(torn, id)
					} else if rng.Intn(2) == 0 {
						k.Append(id, line()+line())
					} else {
						s := line()
						k.Append(id, s[:len(s)/2])
						torn[id] = s[len(s)/2:]
					}
				case op < 7: // parse everything
					k.Parse()
				case op < 8: // a new generation (the old one stays counted)
					k.NewGeneration(id, line())
					delete(torn, id)
				case op < 9: // reparse one generation
					if err := k.Parser.ReparseGen(id, 1); err != nil {
						t.Fatal(err)
					}
				default: // delete a file with unparsed bytes (still counted in lag)
					if deleted < 5 {
						deleted++
						if err := k.Store.Commit(&store.Rec{T: store.TTomb, At: 1_700_000_000_000_000_000 + int64(step), File: id, Path: "/claude/gone"}); err != nil {
							t.Fatal(err)
						}
					}
				}
				lagEqual(t, k, l, label)
			}
			k.Parse()
			lagEqual(t, k, l, "final parse")
		})
	}
}
