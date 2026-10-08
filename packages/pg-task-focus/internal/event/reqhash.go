package event

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// requestFields are the request-level fields that identify or qualify a
// request without being part of what it asks for. They never enter the hash,
// so the same request hashes the same whatever id it carries, whether it is a
// dry run, and the version a client expected.
var requestFields = []string{"id", "dry_run", "expected_version"}

// ReqHash returns the hash that recognizes a repeated request: the lower-case
// hex SHA-256 of the canonical JSON of {"command": command, "fields":
// clientFields}. Canonical means the fields are marshaled, read back as
// generic JSON (integers keep every digit) and marshaled again, so object keys
// are sorted at every depth and the declaration order of a struct does not
// matter. Pass only what the client supplied: a defaulted value such as an
// omitted effective_at MUST be left out, so a retry made later hashes the
// same. The top-level keys id, dry_run and expected_version are dropped.
func ReqHash(command string, clientFields any) (string, error) {
	raw, err := marshalNoEscape(clientFields)
	if err != nil {
		return "", fmt.Errorf("request fields: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", fmt.Errorf("request fields: %w", err)
	}
	if obj, ok := generic.(map[string]any); ok {
		for _, k := range requestFields {
			delete(obj, k)
		}
	}
	canonical, err := marshalNoEscape(map[string]any{"command": command, "fields": generic})
	if err != nil {
		return "", fmt.Errorf("request fields cannot be canonicalized: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// marshalNoEscape is json.Marshal without HTML escaping and without the
// trailing newline an Encoder adds.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
