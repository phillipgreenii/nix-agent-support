package core

import (
	"encoding/json"
	"io"

	"github.com/phillipgreenii/pg-router/conformance"
)

// This file holds the core's level-admission forwarder for a participant's PULL
// request and the shared request / reply helpers of the socket verbs. The
// admission evaluation itself lives in internal/eventqueue (levelgate.go); this
// layer only forwards.
//
// Exported names: Service.AdmitPull.

// AdmitPull is the admission check for a listener's PULL request: it returns a
// *eventqueue.LevelBlockedError naming the failing resource when the
// participant's requirement is not met (rejected pulls are counted), nil
// otherwise. A participant with no registered requirement is always admitted.
func (s *Service) AdmitPull(participantID string) error {
	return s.q.CheckPull(participantID)
}

// decodeVerb reads and schema-checks one verb request into dst.
func decodeVerb(stdin io.Reader, stdout io.Writer, verb, schema string, dst any) bool {
	data, err := io.ReadAll(stdin)
	if err != nil {
		writeBody(stdout, errorReply(verb+": read request: "+err.Error()))
		return false
	}
	if err := conformance.CheckBytes(schema, data); err != nil {
		writeBody(stdout, errorReply(verb+": "+err.Error()))
		return false
	}
	if err := json.Unmarshal(data, dst); err != nil {
		// Unreachable once CheckBytes has passed — see handleStatus's identical note.
		writeBody(stdout, errorReply(verb+": malformed request: "+err.Error()))
		return false
	}
	return true
}

// writeJSONReply marshals reply to stdout.
func writeJSONReply(stdout io.Writer, verb string, reply map[string]any) int {
	body, err := json.Marshal(reply)
	if err != nil { // unreachable: reply holds only JSON-safe scalars
		writeBody(stdout, errorReply(verb+": marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	return conformance.ExitOK
}

// writeJSONValue marshals any JSON-safe value to stdout (writeJSONReply for a
// typed reply rather than a map).
func writeJSONValue(stdout io.Writer, verb string, v any) int {
	body, err := json.Marshal(v)
	if err != nil { // unreachable: v holds only JSON-safe scalars
		writeBody(stdout, errorReply(verb+": marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	return conformance.ExitOK
}
