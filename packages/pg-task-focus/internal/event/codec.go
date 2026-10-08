package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/schemacheck"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/schemas"
)

// SchemaVersion is the only event version this library reads or writes.
const SchemaVersion = 1

// UnknownVersionError is what Decode returns for a line whose v is not
// SchemaVersion. It is never an ordinary decode failure, which the store would
// take for a torn tail: an unknown version refuses to start.
type UnknownVersionError struct{ V int }

func (e *UnknownVersionError) Error() string {
	return fmt.Sprintf("event version %d is not known: this build reads only version %d", e.V, SchemaVersion)
}

// Envelope is the part of an event every line carries. Data is the raw
// payload; Decode fills it with the line's own bytes.
type Envelope struct {
	V           int             `json:"v"`
	ID          ID              `json:"id"`
	At          Instant         `json:"at"`
	EffectiveAt Instant         `json:"effective_at"`
	ReqHash     string          `json:"req_hash,omitempty"`
	Type        Type            `json:"type"`
	Data        json.RawMessage `json:"data"`
}

// Event is one decoded log line. Line is its 1-based position in the log, 0
// while it is unappended.
type Event struct {
	Envelope
	Payload Payload
	Line    int
}

// MaxEventBytes is the longest encoded event line, without its newline.
const MaxEventBytes = 262144

// ErrTooLarge reports text or an encoded event longer than MaxEventBytes.
var ErrTooLarge = errors.New("event text is too large: an event line is at most 262144 bytes")

// ErrInvalidUTF8 reports text that is not valid UTF-8. Such text is refused,
// never rewritten: json.Marshal would store the replacement character in its
// place, and a stored note must be exactly what the operator wrote.
var ErrInvalidUTF8 = errors.New("event text is not valid UTF-8")

// ValidText is the boundary check for free text and for every key or value a
// client supplies: each string MUST be valid UTF-8, and together they MUST fit
// in one event. It returns ErrInvalidUTF8 or ErrTooLarge unwrapped.
func ValidText(strs ...string) error {
	total := 0
	for _, s := range strs {
		if !utf8.ValidString(s) {
			return ErrInvalidUTF8
		}
		total += len(s)
		if total > MaxEventBytes {
			return ErrTooLarge
		}
	}
	return nil
}

// ValidReason checks a skip or override reason and returns it without its
// surrounding whitespace. Blank means strings.TrimSpace(s) == "" (Unicode
// white space, so U+00A0 and U+3000 count) and is an error; so are invalid
// UTF-8 and text over the size limit.
func ValidReason(s string) (string, error) {
	if err := ValidText(s); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", errors.New("a reason MUST NOT be blank: empty or only white space")
	}
	return trimmed, nil
}

// lineSchema compiles the embedded event schema once.
var lineSchema = sync.OnceValues(func() (*schemacheck.Schema, error) {
	return schemacheck.Compile("event.schema.json", schemas.Event())
})

// checkSchema validates one encoded line against the event schema. It is the
// first of the two structural checks a line passes; the Go semantic checks
// follow it and decide what a schema cannot, such as a blank reason or a date
// that does not exist.
func checkSchema(line []byte) error {
	schema, err := lineSchema()
	if err != nil {
		return fmt.Errorf("the embedded event schema does not compile: %w", err)
	}
	if err := schema.Validate(line); err != nil {
		return fmt.Errorf("event line: %w", err)
	}
	return nil
}

// validator is implemented by every payload struct of this package.
type validator interface{ validate() error }

// Decode reads one log line, without its newline. It is strict: an unknown
// field, a missing required field, an instant that is not UTC with
// milliseconds and a payload the Go semantic checks refuse are all errors.
// The version is checked first and alone, so an unknown version is reported
// as *UnknownVersionError whatever else the line holds; the JSON Schema comes
// second, and the Go semantic checks last.
func Decode(line []byte) (Event, error) {
	var probe struct {
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return Event{}, fmt.Errorf("event line is not a JSON object: %w", err)
	}
	if len(probe.V) == 0 {
		return Event{}, errors.New("event line has no v")
	}
	v, err := strconv.Atoi(string(probe.V))
	if err != nil {
		return Event{}, fmt.Errorf("event v %s is not an integer", probe.V)
	}
	if v != SchemaVersion {
		return Event{}, &UnknownVersionError{V: v}
	}

	if len(line) > MaxEventBytes {
		return Event{}, fmt.Errorf("event line of %d bytes: %w", len(line), ErrTooLarge)
	}
	if !utf8.Valid(line) {
		return Event{}, fmt.Errorf("event line: %w", ErrInvalidUTF8)
	}
	if err := checkSchema(line); err != nil {
		return Event{}, err
	}
	var env Envelope
	if err := strictUnmarshal(line, &env); err != nil {
		return Event{}, fmt.Errorf("event envelope: %w", err)
	}
	if _, err := ParseID(string(env.ID)); err != nil {
		return Event{}, fmt.Errorf("event id: %w", err)
	}
	if time.Time(env.At).IsZero() {
		return Event{}, errors.New("event at is required")
	}
	if time.Time(env.EffectiveAt).IsZero() {
		return Event{}, errors.New("event effective_at is required")
	}
	if err := checkReqHash(env.ReqHash); err != nil {
		return Event{}, err
	}
	p, err := decodePayload(env.Type, env.Data)
	if err != nil {
		return Event{}, err
	}
	env.Data = append(json.RawMessage(nil), env.Data...)
	return Event{Envelope: env, Payload: p}, nil
}

// Encode returns the canonical one-line form of e, without a newline: fields
// in a fixed order, instants in the wire form, no raw line break (a line break
// or U+2028 inside text is escaped). The payload is e.Payload, or, when that
// is nil, decoded from e.Data. A zero V is written as SchemaVersion and an
// empty Type is taken from the payload. Encode refuses everything Decode
// would refuse, and returns ErrInvalidUTF8 for any string that is not valid
// UTF-8 and ErrTooLarge for a line longer than MaxEventBytes. No bytes are
// returned with an error.
func Encode(e Event) ([]byte, error) {
	env := e.Envelope
	switch env.V {
	case 0:
		env.V = SchemaVersion
	case SchemaVersion:
	default:
		return nil, &UnknownVersionError{V: env.V}
	}
	if _, err := ParseID(string(env.ID)); err != nil {
		return nil, fmt.Errorf("event id: %w", err)
	}
	if time.Time(env.At).IsZero() {
		return nil, errors.New("event at is required")
	}
	if time.Time(env.EffectiveAt).IsZero() {
		return nil, errors.New("event effective_at is required")
	}
	if err := checkReqHash(env.ReqHash); err != nil {
		return nil, err
	}

	var raw []byte
	switch {
	case e.Payload != nil:
		t := e.Payload.EventType()
		if env.Type != "" && env.Type != t {
			return nil, fmt.Errorf("event type %q does not match its payload %T, which is %q", env.Type, e.Payload, t)
		}
		env.Type = t
		// json.Marshal rewrites invalid UTF-8 to U+FFFD, so the strings are
		// scanned before it can.
		if !stringsValid(reflect.ValueOf(e.Payload)) {
			return nil, ErrInvalidUTF8
		}
		var err error
		if raw, err = marshalNoEscape(e.Payload); err != nil {
			return nil, fmt.Errorf("event payload: %w", err)
		}
	case len(e.Data) > 0:
		if !utf8.Valid(e.Data) {
			return nil, ErrInvalidUTF8
		}
		raw = e.Data
	default:
		return nil, errors.New("event has no payload")
	}

	// Decoding the payload again validates it and puts its keys in the
	// canonical order, so the line written is a line Decode accepts.
	p, err := decodePayload(env.Type, raw)
	if err != nil {
		return nil, err
	}
	if env.Data, err = marshalNoEscape(p); err != nil {
		return nil, fmt.Errorf("event payload: %w", err)
	}
	line, err := marshalNoEscape(env)
	if err != nil {
		return nil, fmt.Errorf("event: %w", err)
	}
	if len(line) > MaxEventBytes {
		return nil, ErrTooLarge
	}
	// A line the library writes MUST be a line it reads, so the line passes
	// the schema before it leaves.
	if err := checkSchema(line); err != nil {
		return nil, err
	}
	return line, nil
}

// decodePayload decodes data strictly as the payload of type t and runs the
// Go semantic checks.
func decodePayload(t Type, data json.RawMessage) (Payload, error) {
	switch t {
	case TypePeriodChanged:
		return decodeAs[PeriodChanged](t, data)
	case TypeProfileChanged:
		return decodeAs[ProfileChanged](t, data)
	case TypeTaskMaterialized:
		return decodeAs[TaskMaterialized](t, data)
	case TypeTaskCompleted:
		return decodeAs[TaskCompleted](t, data)
	case TypeTaskSkipped:
		return decodeAs[TaskSkipped](t, data)
	case TypeTaskMissed:
		return decodeAs[TaskMissed](t, data)
	case TypeTaskWithdrawn:
		return decodeAs[TaskWithdrawn](t, data)
	case TypeTaskReinstated:
		return decodeAs[TaskReinstated](t, data)
	case TypeCycleStarted:
		return decodeAs[CycleStarted](t, data)
	case TypeCyclePaused:
		return decodeAs[CyclePaused](t, data)
	case TypeCycleResumed:
		return decodeAs[CycleResumed](t, data)
	case TypeCycleBoosted:
		return decodeAs[CycleBoosted](t, data)
	case TypeCycleStopped:
		return decodeAs[CycleStopped](t, data)
	case TypeCycleAnnotated:
		return decodeAs[CycleAnnotated](t, data)
	case TypeEventCorrected:
		return decodeAs[EventCorrected](t, data)
	case TypeEventRetracted:
		return decodeAs[EventRetracted](t, data)
	case TypeBatchCommitted:
		return decodeAs[BatchCommitted](t, data)
	case "":
		return nil, errors.New("event type is required")
	}
	return nil, fmt.Errorf("event type %q is not one of the %d known types", string(t), len(Types()))
}

func decodeAs[P interface {
	Payload
	validator
}](t Type, data json.RawMessage) (Payload, error) {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("%s: data is required", t)
	}
	var p P
	if err := strictUnmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: data: %w", t, err)
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", t, err)
	}
	return p, nil
}

// strictUnmarshal decodes exactly one JSON value into v, refusing unknown
// fields and anything after the value.
func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// checkReqHash accepts an empty hash or the lower-case hex SHA-256 ReqHash
// returns.
func checkReqHash(h string) error {
	if h == "" {
		return nil
	}
	if len(h) != 64 {
		return fmt.Errorf("req_hash %q is not 64 lower-case hex digits", h)
	}
	for i := 0; i < len(h); i++ {
		if c := h[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("req_hash %q is not 64 lower-case hex digits", h)
		}
	}
	return nil
}

// stringsValid reports whether every string reachable through exported fields
// of v, including map keys and the bytes of raw JSON, is valid UTF-8.
func stringsValid(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Pointer, reflect.Interface:
		return v.IsNil() || stringsValid(v.Elem())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return utf8.Valid(v.Bytes())
		}
		fallthrough
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !stringsValid(v.Index(i)) {
				return false
			}
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			if !stringsValid(it.Key()) || !stringsValid(it.Value()) {
				return false
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && !stringsValid(v.Field(i)) {
				return false
			}
		}
	}
	return true
}
