package logs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"
)

// The change names of the state changes the daemon owns. A line carries its
// name in the "change" field. The process shell, the socket server, the
// governor and the identity tracker log their own changes through the same
// Logger interface under names of their own (shell_started, lock_held,
// peer_uid_rejected, auth_state_changed, ...); the names below match theirs
// where a change is the same one (lock_held, viewer_changed).
const (
	ChangeDaemonStarted   = "daemon_started"
	ChangeStoreMigrated   = "store_migrated"
	ChangeLockAcquired    = "lock_acquired"
	ChangeLockHeld        = "lock_held"
	ChangeViewerChanged   = "viewer_changed"
	ChangeBudgetPaused    = "budget_paused"
	ChangeBudgetResumed   = "budget_resumed"
	ChangeStoreMovedAside = "store_moved_aside"
	ChangeStoreRefused    = "store_refused"
)

// StructuredLog is the daemon's structured log (daemon.log.jsonl, Loki
// service_name pg-connector-github): one line per state change. It implements
// connectord.Logger, so every library component that takes a Logger logs into
// it, and adds one typed method per change the daemon owns. Safe for
// concurrent use (each line is one O_APPEND write; rotation is under a flock).
type StructuredLog struct {
	Path     string
	MaxBytes int64
	Version  string
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

var _ connectord.Logger = (*StructuredLog)(nil)

// NewStructuredLog returns the structured log inside stateDir, rotating at
// DaemonMaxBytes.
func NewStructuredLog(stateDir, version string) *StructuredLog {
	return &StructuredLog{Path: DaemonLogPath(stateDir), MaxBytes: DaemonMaxBytes, Version: version}
}

func (l *StructuredLog) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Log writes one line for a change: the generic path every library Logger uses.
// The level is warn for a change whose name says it failed or was refused
// (see levelForChange) and info otherwise; the message is the name with spaces.
func (l *StructuredLog) Log(change string, kv ...any) {
	l.write(levelForChange(change), change, strings.ReplaceAll(change, "_", " "), kv)
}

func (l *StructuredLog) write(level, change, msg string, kv []any) {
	write(l.Path, l.MaxBytes, stateLine{
		header: newHeader(l.now(), level, msg, l.Version),
		Change: change,
		fields: kv,
	})
}

// levelForChange is warn for a change named like a fault and info otherwise.
func levelForChange(change string) string {
	for _, sfx := range []string{"_failed", "_rejected", "_timeout", "_held", "_gave_up", "_too_long", "_malformed", "_too_large", "_refused", "_moved_aside"} {
		if strings.HasSuffix(change, sfx) {
			return evlog.LevelWarn
		}
	}
	return evlog.LevelInfo
}

// DaemonStarted logs the daemon's start.
func (l *StructuredLog) DaemonStarted(stateDir, socketPath, configHash string) {
	l.write(evlog.LevelInfo, ChangeDaemonStarted, "daemon started", []any{
		"state_dir", stateDir, "socket_path", socketPath, "config_hash", configHash,
	})
}

// LockAcquired logs that the daemon took the single-instance lock.
func (l *StructuredLog) LockAcquired(lockPath string) {
	l.write(evlog.LevelInfo, ChangeLockAcquired, "daemon lock acquired", []any{"lock", lockPath})
}

// LockHeld logs that another daemon holds the lock, so this one exits.
func (l *StructuredLog) LockHeld(lockPath string) {
	l.write(evlog.LevelWarn, ChangeLockHeld, "daemon lock held by another process", []any{"lock", lockPath})
}

// Migration logs that the store was created or migrated from one schema
// version to another (from is 0 for a new store).
func (l *StructuredLog) Migration(path string, from, to int, took time.Duration) {
	l.write(evlog.LevelInfo, ChangeStoreMigrated, fmt.Sprintf("store migrated from schema %d to %d", from, to), []any{
		"store", path, "from_version", from, "to_version", to, "took", took,
	})
}

// ViewerChanged logs a change of the authenticated viewer (old is "" at the
// first resolution).
func (l *StructuredLog) ViewerChanged(host, oldLogin, newLogin string) {
	l.write(evlog.LevelInfo, ChangeViewerChanged, "viewer changed", []any{
		"old_login", oldLogin, "new_login", newLogin, "host", host,
	})
}

// BudgetPaused logs that background work on a bucket was paused until a time.
func (l *StructuredLog) BudgetPaused(bucket governor.Bucket, reason string, until time.Time) {
	l.write(evlog.LevelWarn, ChangeBudgetPaused, fmt.Sprintf("background work on %s paused", bucket), []any{
		"bucket", string(bucket), "reason", reason, "until", until.UTC().Format(timeLayout),
	})
}

// BudgetResumed logs that a bucket's pause ended.
func (l *StructuredLog) BudgetResumed(bucket governor.Bucket, reason string) {
	l.write(evlog.LevelInfo, ChangeBudgetResumed, fmt.Sprintf("background work on %s resumed", bucket), []any{
		"bucket", string(bucket), "reason", reason,
	})
}

// OnPauseChange is the governor's Options.OnPauseChange: a pause start (until
// non-zero) logs budget_paused, a pause end (until zero) budget_resumed.
func (l *StructuredLog) OnPauseChange(bucket governor.Bucket, reason string, until time.Time) {
	if until.IsZero() {
		l.BudgetResumed(bucket, reason)
		return
	}
	l.BudgetPaused(bucket, reason, until)
}

// StoreMovedAside logs that a corrupt or unmigratable store was moved aside and
// a fresh one created.
func (l *StructuredLog) StoreMovedAside(path, movedTo string, cause error) {
	l.write(evlog.LevelWarn, ChangeStoreMovedAside, "store moved aside and recreated", []any{
		"store", path, "moved_to", movedTo, "cause", cause,
	})
}

// StoreRefused logs that the daemon refused to start because the store's
// schema is newer than this binary (a downgrade); the file is left untouched.
func (l *StructuredLog) StoreRefused(path string, found, supported int) {
	l.write(evlog.LevelError, ChangeStoreRefused, "store refused: schema is newer than this binary", []any{
		"store", path, "found_version", found, "supported_version", supported,
	})
}

// StoreOpened logs what store.Open did, from its Result and error: a migration,
// a move-aside or a refusal each log their line; a store that opened as it was
// logs nothing (it is not a state change).
func (l *StructuredLog) StoreOpened(path string, res store.Result, err error, took time.Duration) {
	switch res.Outcome {
	case store.OutcomeMigrated:
		l.Migration(path, res.FromVersion, res.ToVersion, took)
	case store.OutcomeMovedAside:
		l.StoreMovedAside(path, res.MovedAsideTo, res.Cause)
	case store.OutcomeRefusedNewer:
		found, supported := res.FromVersion, res.ToVersion
		var newer *store.SchemaNewerError
		if errors.As(err, &newer) {
			found, supported = newer.Found, newer.Supported
		}
		l.StoreRefused(path, found, supported)
	}
}

// stateLine is one line of the structured log: the standard header, the change
// name, then the change's own fields flattened in the order given. A field name
// that collides with the header or "change" is written with a "field_" prefix
// so it can never overwrite a standard field.
type stateLine struct {
	header
	Change string
	fields []any
}

var reservedKeys = map[string]bool{"time": true, "level": true, "msg": true, "service": true, "version": true, "pid": true, "change": true}

// MarshalJSON writes the header, "change", then the fields. An odd trailing key
// is written with a null value.
func (s stateLine) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	hdr, err := json.Marshal(s.header)
	if err != nil {
		return nil, err
	}
	b.Write(bytes.TrimSuffix(hdr, []byte("}")))
	field := func(k string, v any) error {
		b.WriteByte(',')
		if err := enc.Encode(k); err != nil {
			return err
		}
		b.Truncate(b.Len() - 1) // the encoder's newline
		b.WriteByte(':')
		if err := enc.Encode(v); err != nil {
			return err
		}
		b.Truncate(b.Len() - 1)
		return nil
	}
	if err := field("change", s.Change); err != nil {
		return nil, err
	}
	for i := 0; i < len(s.fields); i += 2 {
		k := fmt.Sprint(s.fields[i])
		if reservedKeys[k] {
			k = "field_" + k
		}
		var v any
		if i+1 < len(s.fields) {
			v = jsonValue(s.fields[i+1])
		}
		if err := field(k, v); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// jsonValue makes a field value encodable the way a reader expects: an error as
// its text, a duration as its String form; everything else as encoding/json
// would (an unencodable value falls back to its fmt text).
func jsonValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case error:
		return x.Error()
	case time.Duration:
		return x.String()
	case string, bool, int, int64, uint64, float64, time.Time, int32, uint, uint32:
		return x
	}
	if _, err := json.Marshal(v); err != nil {
		return fmt.Sprint(v)
	}
	return v
}
