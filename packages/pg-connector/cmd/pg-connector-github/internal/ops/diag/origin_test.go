package diag

import (
	"reflect"
	"sync"
	"testing"
)

// hintLog records the origin hints the engine receives.
type hintLog struct {
	mu    sync.Mutex
	notes [][3]string
}

func (h *hintLog) NoteOrigin(node, group, origin string) {
	h.mu.Lock()
	h.notes = append(h.notes, [3]string{node, group, origin})
	h.mu.Unlock()
}

func TestRefreshNotesTheRefreshOriginForEveryGroupItQueues(t *testing.T) {
	e := newEnv(t)
	e.seed(node, "detail", "conversation", "files", "commits", "pending", "runs")
	h := &hintLog{}
	e.rig.Svc.SetOriginHints(h)

	res := e.refresh("pr", `{"id":"`+e.ref(node).String()+`"}`)
	var want [][3]string
	for _, g := range res.Queued[0].Groups {
		want = append(want, [3]string{node, g, "refresh"})
	}
	if len(want) != len(sixGroups) {
		t.Fatalf("queued %v", res.Queued[0].Groups)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !reflect.DeepEqual(h.notes, want) {
		t.Errorf("notes = %v, want one refresh note per queued group %v", h.notes, want)
	}
}

func TestRefreshOfAnUnseenPRNotesTheRefreshOriginOnlyForTheSummaryItQueues(t *testing.T) {
	e := newEnv(t)
	e.rig.Resolver.Know(e.ref(node), node)
	h := &hintLog{}
	e.rig.Svc.SetOriginHints(h)
	// The reference is resolved and accessed (which creates the entity row), and
	// only the summary is queued; the note names exactly that group.
	res := e.refresh("pr", `{"id":"`+e.ref(node).String()+`"}`)
	if !reflect.DeepEqual(res.Queued[0].Groups, []string{"summary"}) {
		t.Fatalf("queued groups = %v", res.Queued[0].Groups)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if want := [][3]string{{node, "summary", "refresh"}}; !reflect.DeepEqual(h.notes, want) {
		t.Errorf("notes = %v, want %v", h.notes, want)
	}
}
