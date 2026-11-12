package internal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// claimFakeBD is a stateful stand-in for bd's claim semantics: it holds an
// assignee per issue and, like bd 1.3.1, refuses close and update from an
// actor other than the holder unless --force is given. The refusal is written
// the way bd writes it: a {"data":{"error":...}} envelope on stdout together
// with a non-zero exit.
type claimFakeBD struct {
	actor    string
	assignee map[string]string
	closed   map[string]bool
	calls    int
}

func newClaimFakeBD(actor string) *claimFakeBD {
	return &claimFakeBD{actor: actor, assignee: map[string]string{"tp-1": "someone-else"}, closed: map[string]bool{}}
}

func (f *claimFakeBD) Run(_ context.Context, args ...string) (string, error) {
	f.calls++
	verb := args[0]
	if verb != "close" && verb != "update" {
		return "", fmt.Errorf("claimFakeBD: unexpected verb %q", verb)
	}
	id := args[len(args)-1]
	holder := f.assignee[id]
	force := false
	for _, a := range args {
		if a == "--force" {
			force = true
		}
	}
	if holder != "" && holder != f.actor && !force {
		msg := fmt.Sprintf("cannot %s %s: assignee is %q, actor is %q; reclaim or use --force to override", verb, id, holder, f.actor)
		return fmt.Sprintf(`{"data":{"error":%q},"schema_version":1}`, msg), errors.New("exit status 1")
	}
	if verb == "close" {
		f.closed[id] = true
		return `{"data":[{"id":"` + id + `","status":"closed"}],"schema_version":1}`, nil
	}
	return `{"data":[{"id":"` + id + `","status":"open"}],"schema_version":1}`, nil
}

func (f *claimFakeBD) Workspace() (string, error) { return "/fake/workspace", nil }

func TestBackend_Close_CrossActorClaimRefusal_IsNotUnavailable(t *testing.T) {
	fake := newClaimFakeBD("closer")
	b := New(fake)
	err := b.Close(context.Background(), "tp-1", "merged")
	if err == nil {
		t.Fatal("expected the cross-actor close to be refused")
	}
	if errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v: a claim refusal must not be unavailable (the router retries it)", err)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
	if got := scriptout.CodeForError(err); got != "invalid_argument" {
		t.Fatalf("wire code = %q, want invalid_argument", got)
	}
	if !strings.Contains(err.Error(), `assignee is "someone-else"`) {
		t.Fatalf("err = %v, want bd's own message preserved", err)
	}
	if fake.closed["tp-1"] {
		t.Fatal("the refused close must not close the issue")
	}
}

func TestBackend_Update_CrossActorClaimRefusal_IsNotUnavailable(t *testing.T) {
	b := New(newClaimFakeBD("closer"))
	err := b.Transition(context.Background(), "tp-1", "open")
	if err == nil || errors.Is(err, scriptout.ErrUnavailable) || !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument and not unavailable", err)
	}
}

func TestBackend_Close_HolderAndForce_StillSucceed(t *testing.T) {
	fake := newClaimFakeBD("someone-else")
	if err := New(fake).Close(context.Background(), "tp-1", "done"); err != nil {
		t.Fatalf("the holder's own close failed: %v", err)
	}
	fake = newClaimFakeBD("closer")
	if err := New(fake).CloseForce(context.Background(), "tp-1", "done"); err != nil || !fake.closed["tp-1"] {
		t.Fatalf("a forced close failed: err=%v closed=%v", err, fake.closed)
	}
}

func TestClassifyBDErrorMessage_AssigneeMentionAloneStaysUnavailable(t *testing.T) {
	err := classifyBDErrorMessage("failed to read assignee is blank in storage")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}
