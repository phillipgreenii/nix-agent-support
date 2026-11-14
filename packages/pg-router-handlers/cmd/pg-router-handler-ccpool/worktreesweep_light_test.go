package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
)

// lightCC answers List with full rows and ListLight with the same rows minus
// the facets only the full list resolves (the live pane cwd). It counts calls.
type lightCC struct {
	*orphanCC
	full, light []ccpool.Session
	lightErr    error
	listCalls   int
	lightCalls  int
}

func (c *lightCC) List(context.Context) ([]ccpool.Session, error) {
	c.listCalls++
	return c.full, nil
}

func (c *lightCC) ListLight(context.Context) ([]ccpool.Session, error) {
	c.lightCalls++
	if c.lightErr != nil {
		return nil, c.lightErr
	}
	return c.light, nil
}

// The held-by-row decision on the facet-free rows equals the decision on the
// full rows for every case worktreeHeldByRow handles, because it reads only
// CloseReason, Live, Meta[bead] and CWD (the launch dir in the light list).
func TestWorktreeHeldByRow_sameOnLightAndFullRows(t *testing.T) {
	wt := t.TempDir()
	c := leakedWorktree{bead: "b-1", path: wt}
	cases := []struct {
		name string
		row  ccpool.Session
		want bool
	}{
		{"bead meta match, open", ccpool.Session{Meta: map[string]string{ccpool.MetaKeyBead: "b-1"}}, true},
		{"bead meta match, closed and not live", ccpool.Session{CloseReason: "idle_ttl", Meta: map[string]string{ccpool.MetaKeyBead: "b-1"}}, false},
		{"bead meta match, closed but live", ccpool.Session{CloseReason: "idle_ttl", Live: true, Meta: map[string]string{ccpool.MetaKeyBead: "b-1"}}, true},
		{"launch cwd is the worktree, live", ccpool.Session{Live: true, CWD: wt}, true},
		{"launch cwd is the worktree, closed", ccpool.Session{CloseReason: "operator", CWD: wt}, false},
		{"other bead and other cwd", ccpool.Session{Live: true, CWD: t.TempDir(), Meta: map[string]string{ccpool.MetaKeyBead: "b-2"}}, false},
		{"no cwd, no bead", ccpool.Session{Live: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full := tc.row
			light := tc.row // the light row carries the same fields worktreeHeldByRow reads
			cc := &lightCC{orphanCC: &orphanCC{}, full: []ccpool.Session{full}, light: []ccpool.Session{light}}
			gotLight, err := ccpool.ListPreferLight(context.Background(), cc)
			if err != nil {
				t.Fatal(err)
			}
			gotFull, _ := cc.List(context.Background())
			if cc.lightCalls != 1 {
				t.Errorf("ListLight calls = %d, want 1", cc.lightCalls)
			}
			l, f := worktreeHeldByRow(gotLight, c), worktreeHeldByRow(gotFull, c)
			if l != f || l != tc.want {
				t.Errorf("light=%v full=%v want=%v", l, f, tc.want)
			}
		})
	}
}

// A live session whose pane drifted into the worktree has the launch dir (not
// the worktree) as its light-list cwd, so the row does not protect it; the
// live-process working-directory probe does.
func TestWorktreeSweep_driftedPaneProtectedByProcessProbe(t *testing.T) {
	wt := t.TempDir()
	c := leakedWorktree{bead: "b-1", path: wt}
	rows := []ccpool.Session{{Live: true, CWD: t.TempDir(), Meta: map[string]string{ccpool.MetaKeyBead: "b-9"}}}
	if worktreeHeldByRow(rows, c) {
		t.Fatal("a drifted row must not be held by the row check")
	}
	if !worktreeHeldByProcess([]string{wt + "/sub"}, c) {
		t.Fatal("the process probe must protect a worktree that is a live process's cwd")
	}
}

func TestListPreferLight_fallbacks(t *testing.T) {
	rows := []ccpool.Session{{ExternalID: "full"}}
	t.Run("unsupported flag falls back to List", func(t *testing.T) {
		cc := &lightCC{orphanCC: &orphanCC{}, full: rows, lightErr: fmt.Errorf("%w: old", ccpool.ErrNoFacetsUnsupported)}
		got, err := ccpool.ListPreferLight(context.Background(), cc)
		if err != nil || len(got) != 1 || cc.listCalls != 1 {
			t.Fatalf("got=%v err=%v listCalls=%d", got, err, cc.listCalls)
		}
	})
	t.Run("other failure is returned, List not tried", func(t *testing.T) {
		boom := errors.New("boom")
		cc := &lightCC{orphanCC: &orphanCC{}, full: rows, lightErr: boom}
		if _, err := ccpool.ListPreferLight(context.Background(), cc); !errors.Is(err, boom) || cc.listCalls != 0 {
			t.Fatalf("err=%v listCalls=%d", err, cc.listCalls)
		}
	})
	t.Run("runner without ListLight uses List", func(t *testing.T) {
		cc := &orphanCC{sessions: rows}
		got, err := ccpool.ListPreferLight(context.Background(), cc)
		if err != nil || len(got) != 1 {
			t.Fatalf("got=%v err=%v", got, err)
		}
	})
}
