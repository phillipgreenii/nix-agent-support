package core

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/pr-pool/schemas"
)

// Discovery artifact names under Config.LogDir. LogDir is where pr-pool already
// keeps its own runtime state (the JSONL event log, PR_POOL_LOG_DIR), so the
// socket and its discovery record live beside it rather than inventing a second
// state root.
const (
	// SocketName is the unix-domain socket the core listens on.
	SocketName = "core.sock"
	// RecordName is the discovery record a CLI reads to find the running core.
	RecordName = "core.json"
)

// DefaultProbeTimeout bounds a liveness probe (probe) and Dial's own connect
// attempt (Task 3.10 Interfaces: "probe <= 1s") — a local unix-domain socket
// connect is effectively instantaneous, so both stay well under
// DefaultCallTimeout: connecting is not itself a call, and a hung/absent peer
// must be reported quickly rather than pinning a caller for a full call budget.
const DefaultProbeTimeout = 1 * time.Second

// DefaultCallTimeout bounds one Client.Call round-trip (a client waiting for a
// reply) when CallOptions.CallTimeout is zero. It stops a hung peer from pinning
// a connection or a goroutine forever. An enqueue is a local, durable append —
// milliseconds — so this is a generous ceiling, not a latency budget. Task 4.0's
// poller defaults to the same duration (Task 3.10 Interfaces), by convention, not
// by propagation — see serverCallDeadline in core.go for the server-side half of
// that convention.
const DefaultCallTimeout = 5 * time.Second

// maxSocketPathLen guards the platform limit on a unix socket path
// (sockaddr_un.sun_path is 104 bytes on darwin, 108 on Linux). net.Listen reports
// this only as "invalid argument", which is useless to an operator, so Listen
// checks it up front and names the fix.
const maxSocketPathLen = 100

// ErrNoRunningCore is returned when no running core can be located: no injected
// socket, and no live socket service discoverable under LogDir.
//
// It is TERMINAL — the CLI reports it and exits non-zero, and NEVER auto-starts a
// core (ADR 0036; the former OQ-AUTOSTART, resolved 2026-07-28). The rationale,
// recorded here because this is the error every locate path funnels through:
//
//   - spawning is the larger behavior commitment, and it is easy to ADD later
//     while un-spawning it is not;
//   - it would put daemon lifecycle ownership in a CLI entry point (a callback
//     invoked by a participant), where nothing owns the daemon's shutdown;
//   - it needs a lock to stop two concurrent callbacks racing to spawn two cores
//     over one socket path;
//   - keeping it an error leaves "is a core running?" observable from the
//     caller's exit code, which a spawn would silently erase.
var ErrNoRunningCore = errors.New("no running core")

// ErrAlreadyRunning is returned by Listen when a live core already answers on the
// socket — two cores must never share one socket path.
var ErrAlreadyRunning = errors.New("a core is already listening on this socket")

// Ref identifies a located running core: the socket to reach it on and the auth
// token to present. It is what a core-issued callback carries as arguments
// (interfaces.md "Callback": the participant never assembles these itself).
type Ref struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
}

// SocketPath returns the core socket path under logDir.
func SocketPath(logDir string) string { return filepath.Join(logDir, SocketName) }

// RecordPath returns the discovery record path under logDir.
func RecordPath(logDir string) string { return filepath.Join(logDir, RecordName) }

// record is the discovery record: how a CLI in the same trust domain (same user,
// same LogDir) finds the running core. The behavior docs pin only that the CLI
// "discovers the running socket service" — the file's shape is a realization
// choice, so it is deliberately NOT an interface message schema.
type record struct {
	SchemaVersion string    `json:"schemaVersion"`
	Socket        string    `json:"socket"`
	Token         string    `json:"token"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"startedAt"`
}

// newToken mints the per-core auth token: 32 bytes of CSPRNG entropy, hex. The
// token is the ONLY thing standing between a socket request and the core's
// queue, so it is never derived from anything predictable (pid, path, time).
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("core: mint token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// writeRecord persists the discovery record 0600 (it carries the auth token) via
// a temp file + rename, so a reader never observes a half-written record.
func writeRecord(path string, r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("core: marshal discovery record: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("core: write discovery record: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("core: publish discovery record: %w", err)
	}
	return nil
}

// readRecord loads the discovery record. A missing record means no core has
// published itself, which is ErrNoRunningCore rather than an I/O failure.
func readRecord(path string) (record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return record{}, fmt.Errorf("%w: no discovery record at %s", ErrNoRunningCore, path)
		}
		return record{}, fmt.Errorf("core: read discovery record: %w", err)
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return record{}, fmt.Errorf("%w: unreadable discovery record at %s: %v", ErrNoRunningCore, path, err)
	}
	if r.Socket == "" || r.Token == "" {
		return record{}, fmt.Errorf("%w: incomplete discovery record at %s", ErrNoRunningCore, path)
	}
	return r, nil
}

// Discover locates the running core under logDir: it reads the discovery record
// and PROVES the core is live by connecting to the socket. A record left behind
// by a crashed core is therefore reported as ErrNoRunningCore, not handed out as
// a Ref that every later call would fail against.
//
// It never starts a core (ErrNoRunningCore, ADR 0036).
func Discover(logDir string) (Ref, error) {
	r, err := readRecord(RecordPath(logDir))
	if err != nil {
		return Ref{}, err
	}
	if err := probe(r.Socket, DefaultProbeTimeout); err != nil {
		return Ref{}, err
	}
	return Ref{Socket: r.Socket, Token: r.Token}, nil
}

// probe reports whether something is accepting connections on socket within
// timeout, mapping a dead/stale socket to ErrNoRunningCore.
func probe(socket string, timeout time.Duration) error {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return fmt.Errorf("%w: socket %s is not accepting connections: %v", ErrNoRunningCore, socket, err)
	}
	return conn.Close()
}

// wireRequest is one socket TRANSPORT frame: the participant/operator subcommand
// to run, the auth token, and the subcommand's JSON request payload (what the
// CLI transport would put on stdin).
//
// This framing is the transport contract, NOT the message schema: interfaces.md
// explicitly allows a participant to "speak a gRPC or in-code transport contract
// over the socket and still conform, so long as it carries the same message
// schema". The payload is passed through byte-for-byte to the SAME
// conformance.Participant boundary the stdin/stdout transport uses, so the schema
// is validated in exactly one place.
type wireRequest struct {
	Token      string          `json:"token"`
	Subcommand string          `json:"subcommand"`
	Payload    json.RawMessage `json:"payload"`
}

// wireResponse carries the coarse exit code plus the reply body the subcommand
// wrote (interfaces.md: "the rich outcome is in the JSON reply"). Reply is null
// when the subcommand wrote no body (the legal busy case, exit 9).
type wireResponse struct {
	ExitCode int             `json:"exitCode"`
	Reply    json.RawMessage `json:"reply"`
}

// jsonNull is the Reply value used when a subcommand wrote no body — an empty
// json.RawMessage is not valid JSON and would fail to marshal.
var jsonNull = json.RawMessage("null")

// Client is a single-use connection to a running core. One connection carries one
// request/reply, mirroring the CLI transport's one-invocation-one-message shape.
type Client struct {
	conn  net.Conn
	token string

	// inFlight guards ErrCallInFlight (Task 3.10 Binding decisions): Client is
	// single-use in every production caller today, so this is near-vacuous
	// defense-in-depth for a future multi-use client, not a load-bearing guard —
	// the load-bearing single-in-flight enforcement is Task 4.0's poller, out of
	// scope here.
	inFlight int32
}

// Dial connects to the core named by ref, bounding the connect attempt itself at
// probeTimeout (Task 3.10 Interfaces: a local unix-domain socket connect is
// effectively instantaneous, so it stays in the same short budget as probe's own
// liveness check, distinct from a Call's longer CallOptions.CallTimeout). A dial
// failure is ErrNoRunningCore: an injected socket that nothing answers on is
// indistinguishable, to the caller, from no core at all — and the answer is the
// same (report it, never spawn).
func Dial(ref Ref, probeTimeout time.Duration) (*Client, error) {
	if ref.Socket == "" {
		return nil, fmt.Errorf("%w: no socket to dial", ErrNoRunningCore)
	}
	if probeTimeout <= 0 {
		probeTimeout = DefaultProbeTimeout
	}
	conn, err := net.DialTimeout("unix", ref.Socket, probeTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot reach socket %s: %v", ErrNoRunningCore, ref.Socket, err)
	}
	return &Client{conn: conn, token: ref.Token}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// CallOptions configures one Client.Call. It is its own type — not the
// unrelated Options Listen already declares (internal/core/core.go) — because
// reusing that name for Call's parameter would be a same-package duplicate-type
// compile error, not a style choice (Task 3.10 Interfaces).
type CallOptions struct {
	// CallTimeout bounds one round trip (send the request, read the reply).
	// Zero means DefaultCallTimeout.
	CallTimeout time.Duration
}

// ErrCallInFlight is returned when Call is invoked while a previous Call on the
// SAME Client has not yet returned. See the inFlight field's doc on Client: this
// guard is documented defense-in-depth for a future multi-use client, since every
// production Client today is single-use (one Dial, one Call, Close).
var ErrCallInFlight = errors.New("core: a call is already in flight on this client")

// Call sends one subcommand request and returns the reply body and the coarse
// exit code (0 ok / 1 error / 2 usage / 9 busy). The payload is the subcommand's
// JSON request — the same bytes the stdin/stdout transport would carry.
//
// ctx is not decorative (Task 3.10 Interfaces): Call's blocking operations (the
// connection read/write) select on ctx.Done() as well as the deadline derived
// from opts.CallTimeout, so an external cancellation (e.g. the CLI's own signal
// handling) unblocks promptly instead of waiting out the full timeout. There is
// no ctx-aware net.Conn API, so a watcher goroutine collapses the connection
// deadline to "now" the instant ctx is done, which aborts any in-flight
// read/write with an immediate timeout error instead.
func (c *Client) Call(ctx context.Context, subcommand string, payload []byte, opts CallOptions) (reply []byte, exitCode int, err error) {
	if !atomic.CompareAndSwapInt32(&c.inFlight, 0, 1) {
		return nil, 0, fmt.Errorf("%w: %s", ErrCallInFlight, subcommand)
	}
	defer atomic.StoreInt32(&c.inFlight, 0)

	timeout := opts.CallTimeout
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, 0, fmt.Errorf("core: set deadline: %w", err)
	}

	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.conn.SetDeadline(time.Now()) // abort any in-flight read/write immediately
		case <-watchDone:
		}
	}()

	body := json.RawMessage(payload)
	if len(body) == 0 {
		body = jsonNull
	}
	req := wireRequest{Token: c.token, Subcommand: subcommand, Payload: body}
	if err := json.NewEncoder(c.conn).Encode(req); err != nil {
		if ctx.Err() != nil {
			return nil, 0, fmt.Errorf("core: send %s request: %w", subcommand, ctx.Err())
		}
		return nil, 0, fmt.Errorf("core: send %s request: %w", subcommand, err)
	}
	var resp wireResponse
	if err := json.NewDecoder(c.conn).Decode(&resp); err != nil {
		if ctx.Err() != nil {
			return nil, 0, fmt.Errorf("core: read %s reply: %w", subcommand, ctx.Err())
		}
		return nil, 0, fmt.Errorf("core: read %s reply: %w", subcommand, err)
	}
	if string(resp.Reply) == "null" {
		return nil, resp.ExitCode, nil
	}
	return resp.Reply, resp.ExitCode, nil
}

// authorized compares a presented token against want in constant time, so a
// rejected request leaks nothing about how much of the token matched.
func authorized(presented, want string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(want)) == 1
}

// errorReply is the PROTOCOL-level failure envelope: `{schemaVersion, error}`.
//
// It is deliberately outside the per-message reply schemas — those set
// additionalProperties:false and declare no `error` field, because they describe
// a subcommand's OUTCOME, not a transport/lifecycle refusal. This matches
// conformance.ReferenceHandler, which writes the same envelope for a
// malformed/not-started dispatch.
func errorReply(msg string) []byte {
	b, err := json.Marshal(map[string]any{"schemaVersion": schemas.SchemaVersion, "error": msg})
	if err != nil { // unreachable: two strings always marshal
		return []byte(`{"schemaVersion":"1","error":"internal marshal failure"}`)
	}
	return b
}
