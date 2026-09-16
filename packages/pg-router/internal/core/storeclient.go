// storeclient.go implements the wire-forwarding kvstore.Store used once a
// KindStorage participant registers (Service.store's second state, Binding
// decision 4). It reuses internal/wireclient.Runner — the SAME
// argv-invocation mechanism roleListener / wireclient.Client already use to
// reach a registered HANDLER participant's own command for handler.dispatch
// — for the storage participant's own get/put/delete verbs, rather than
// duplicating a parallel subprocess-invocation type. internal/kvstore itself
// cannot own this implementation (Task 6.7 Files): internal/core already
// depends on internal/kvstore, so a reverse dependency would cycle.
//
// # BLOCKING GAP (curation finding, verified against the live repo — see this
// task's own packet body / register.go's Service.Register doc for the full
// citation trail; the same class of gap as pg2-oju6w.4)
//
// No production wiring resolves a StoreCommandFor for a registered
// KindStorage participant today: internal/roles/roles.go's own package doc
// and internal/config/config.go's Type/CommandConfig handling both state
// that a role no longer declares a backing command at all ("no successor"),
// schemas/cli.register.schema.json has no command field a participant could
// supply, and core.go's ingestCallbackFor returns "" for every kind but
// KindSource, explicitly noting storage is core-initiated with no callback
// of its own. Options.StoreCommand therefore stays nil in every real
// deployment; wireStore.call reports that plainly (mirroring
// wireclient.Client.Dispatch's identical "no CommandFor configured" report)
// rather than silently no-op'ing or claiming a live round trip that cannot
// happen. Building the missing command-resolution seam is explicitly OUT OF
// SCOPE for this task (escalate to the docket design/operator instead) —
// this file's job is only to make the Go-level shape exist, compile, and be
// independently verifiable: Task 6.7's own fake-participant unit test
// supplies both a stub StoreCommandFor and a stub wireclient.Runner, never a
// live registered participant.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/pg-router/internal/kvstore"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
	"github.com/phillipgreenii/pg-router/schemas"
)

// StoreCommandFor resolves the argv PREFIX (before the get/put/delete verb
// is appended) to invoke for the registered KindStorage participant with
// the given id — the storage-side counterpart to wireclient.CommandFor
// (INTF-HANDLER's own resolver), following the identical shape: a
// deployment concern this package never resolves on its own initiative. See
// this file's package doc for why no caller supplies one today.
type StoreCommandFor func(id string) ([]string, error)

// wireStore is the wire-forwarding kvstore.Store implementation Service.
// Register swaps in once a KindStorage participant registers.
type wireStore struct {
	id      string
	command StoreCommandFor
	runner  wireclient.Runner
}

var _ kvstore.Store = (*wireStore)(nil)

// newWireStore returns a kvstore.Store that forwards Get/Put/Delete to the
// registered KindStorage participant id via command's resolved argv,
// invoked through runner (defaulting to wireclient.OSRunner{} — the SAME
// production default wireclient.Client uses — when runner is nil).
func newWireStore(id string, command StoreCommandFor, runner wireclient.Runner) kvstore.Store {
	if runner == nil {
		runner = wireclient.OSRunner{}
	}
	return &wireStore{id: id, command: command, runner: runner}
}

// storeRequestWire / storeReplyWire mirror schemas/store.request.schema.json
// and schemas/store.reply.schema.json — the concrete Go types this client
// encodes/decodes, the same pattern wireclient's own dispatchRequest/
// dispatchReply use for handler.dispatch. OK/Value are pointers so a
// present-but-false / present-but-null wire value is distinguishable from
// an absent one.
type storeRequestWire struct {
	SchemaVersion string  `json:"schemaVersion"`
	ID            string  `json:"id"`
	Op            string  `json:"op"`
	Key           string  `json:"key"`
	Value         *string `json:"value,omitempty"`
}

type storeReplyWire struct {
	SchemaVersion string  `json:"schemaVersion"`
	ID            string  `json:"id"`
	OK            *bool   `json:"ok,omitempty"`
	Value         *string `json:"value,omitempty"`
}

// Get implements kvstore.Store: forwards to the registered participant's
// `get` verb. A wire reply carrying {value: null} — or omitting value
// entirely; JSON cannot distinguish the two once decoded into *string, and
// neither does this package's own handleGet reply on the SERVER side — maps
// to ok=false with a nil error (Binding decision 4): kvstore.Store's own
// documented "absent" idiom, not a Go error. An explicit {ok:false} reply,
// or any transport-level failure, maps to a Go error instead.
func (w *wireStore) Get(key string) (value string, ok bool, err error) {
	reply, err := w.call(SubcommandGet, key, nil)
	if err != nil {
		return "", false, err
	}
	if reply.OK != nil && !*reply.OK {
		return "", false, fmt.Errorf("kvstore: storage participant %q declined get: ok=false", w.id)
	}
	if reply.Value == nil {
		return "", false, nil
	}
	return *reply.Value, true, nil
}

// Put implements kvstore.Store: forwards to the registered participant's
// `put` verb.
func (w *wireStore) Put(key, value string) error {
	reply, err := w.call(SubcommandPut, key, &value)
	if err != nil {
		return err
	}
	if reply.OK != nil && !*reply.OK {
		return fmt.Errorf("kvstore: storage participant %q declined put: ok=false", w.id)
	}
	return nil
}

// Delete implements kvstore.Store: forwards to the registered participant's
// `delete` verb. Per kvstore.Store's own doc, deleting an absent key is a
// no-op, not an error — a participant that itself honors that reports
// ok=true (or omits ok); only an explicit {ok:false} or a transport failure
// surfaces as a Go error here.
func (w *wireStore) Delete(key string) error {
	reply, err := w.call(SubcommandDelete, key, nil)
	if err != nil {
		return err
	}
	if reply.OK != nil && !*reply.OK {
		return fmt.Errorf("kvstore: storage participant %q declined delete: ok=false", w.id)
	}
	return nil
}

// call sends one store.request to w's registered participant and decodes
// its store.reply, reusing wireclient.Runner's argv-invocation mechanism —
// the SAME Runner interface wireclient.Client uses for handler.dispatch —
// rather than a duplicated subprocess-invocation type. verb is appended to
// the resolved argv exactly as wireclient.Dispatch appends "dispatch"; here
// it is one of SubcommandGet/Put/Delete, which double as the participant's
// own expected subcommand names (Binding decision 3: the three verbs mirror
// the core's own socket subcommands).
func (w *wireStore) call(verb, key string, value *string) (storeReplyWire, error) {
	if w.command == nil {
		return storeReplyWire{}, fmt.Errorf("kvstore: no command-resolution configured for storage participant %q", w.id)
	}
	argv, err := w.command(w.id)
	if err != nil {
		return storeReplyWire{}, fmt.Errorf("kvstore: resolve command for storage participant %q: %w", w.id, err)
	}
	if len(argv) == 0 {
		return storeReplyWire{}, fmt.Errorf("kvstore: storage participant %q resolved an empty command", w.id)
	}
	id, err := newStoreRequestID()
	if err != nil {
		return storeReplyWire{}, fmt.Errorf("kvstore: mint request id: %w", err)
	}
	req := storeRequestWire{SchemaVersion: schemas.SchemaVersion, ID: id, Op: verb, Key: key, Value: value}
	body, err := json.Marshal(req)
	if err != nil {
		return storeReplyWire{}, fmt.Errorf("kvstore: encode store.request: %w", err)
	}

	stdout, code, err := w.runner.Run(context.Background(), append(argv, verb), body)
	if err != nil {
		return storeReplyWire{}, err
	}
	if code != 0 {
		return storeReplyWire{}, fmt.Errorf("kvstore: storage participant %q exited %d", w.id, code)
	}
	if len(stdout) == 0 {
		return storeReplyWire{}, fmt.Errorf("kvstore: storage participant %q: empty reply on exit 0", w.id)
	}
	var reply storeReplyWire
	if err := json.Unmarshal(stdout, &reply); err != nil {
		return storeReplyWire{}, fmt.Errorf("kvstore: storage participant %q: decode reply: %w", w.id, err)
	}
	return reply, nil
}

// errNoOSRandom exists only so newStoreRequestID has a named sentinel to
// wrap — crypto/rand.Read failing is effectively unreachable on any real
// platform, but this package reports rather than guesses per its own
// report-don't-guess convention.
var errNoOSRandom = errors.New("kvstore: crypto/rand unavailable")

// newStoreRequestID mints a fresh store.request tracking id, the same "N
// random hex chars behind a short prefix" shape wireclient's own
// (unexported) newDispatchID uses for handler.dispatch — the identical
// PATTERN re-applied here, not a shared call, the same relationship
// wireclient's own doc comment describes for its "dsp-" prefix.
func newStoreRequestID() (string, error) {
	b := make([]byte, 6) // 6 bytes -> 12 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("%w: %v", errNoOSRandom, err)
	}
	return "st-" + hex.EncodeToString(b), nil
}
