package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
	"github.com/phillipgreenii/pg-rescue/internal/triage"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

type tr struct {
	item triage.Item
	err  error
}

func (t tr) Show(string) (triage.Item, error) { return t.item, t.err }
func (tr) Comments(string) ([]string, error)  { return nil, nil }
func (tr) Claim(string, string) error         { return nil }
func (tr) Comment(string, string) error       { return nil }
func (tr) Close(string, string) error         { return nil }
func (tr) Release(string) error               { return nil }
func (tr) HandOver(string) error              { return nil }

func TestExitCodes(t *testing.T) {
	env := func(k string) string {
		if k == "PG_ROUTER_ITEM_ID" {
			return "x-1"
		}
		return ""
	}
	none := func(string) string { return "" }
	var out, errb bytes.Buffer
	if got := run(nil, none, "h", tr{}, &out, &errb); got != 2 {
		t.Fatalf("no item id: %d", got)
	}
	if got := run([]string{"--bogus"}, env, "h", tr{}, &out, &errb); got != 2 {
		t.Fatalf("bad flag: %d", got)
	}
	skipped := tr{item: triage.Item{ID: "x-1", Status: "closed"}}
	out.Reset()
	if got := run(nil, env, "h", skipped, &out, &errb); got != 0 || !strings.Contains(out.String(), "x-1 skipped") {
		t.Fatalf("skipped: %d %q", got, out.String())
	}
	if got := run(nil, env, "h", tr{err: errors.New("down")}, &out, &errb); got != 1 {
		t.Fatalf("tracker failure: %d", got)
	}
}

type actorTr struct {
	tr
	actor *string
}

func (a actorTr) WithActor(s string) triage.Tracker { *a.actor = s; return a.tr }

func TestActorIsThreadedToTracker(t *testing.T) {
	var got string
	env := func(k string) string {
		switch k {
		case "PG_ROUTER_ITEM_ID":
			return "x-1"
		case "BEADS_ACTOR":
			return "boss"
		}
		return ""
	}
	var out, errb bytes.Buffer
	skipped := tr{item: triage.Item{ID: "x-1", Status: "closed"}}
	run(nil, env, "h", actorTr{tr: skipped, actor: &got}, &out, &errb)
	if got != "boss" {
		t.Fatalf("actor = %q", got)
	}
	got = ""
	run(nil, func(k string) string {
		if k == "PG_ROUTER_ITEM_ID" {
			return "x-1"
		}
		return ""
	}, "h", actorTr{tr: skipped, actor: &got}, &out, &errb)
	if got != "pg-rescue-triage" {
		t.Fatalf("default actor = %q", got)
	}
}
