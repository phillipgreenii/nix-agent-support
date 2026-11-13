package main

import (
	"strings"
	"testing"
)

// writeFakeBackend registers an in-process fake backend named name that
// discards the request and answers stdout verbatim, regardless of the op it
// was called with. Used to exercise Dispatch/FanOutAuthStatus/
// FanOutConfigValidate against a backend invocation without needing a full
// pg-connector-*-github style binary.
func writeFakeBackend(t *testing.T, name, stdout string) {
	t.Helper()
	registerFake(t, name, func(string) string { return stdout + "\n" })
}

// writeEchoFakeBackend registers a fake that replies with a success envelope
// whose result is the ENTIRE received request object verbatim — used to prove
// Dispatch/DispatchTargeted/dispatchScm actually attach a backend's own
// registered config block to the outgoing wire request (bead pg2-2j5ac.28.1),
// since writeFakeBackend's own fixed canned response discards the request
// entirely and so cannot observe it.
func writeEchoFakeBackend(t *testing.T, name string) {
	t.Helper()
	registerFake(t, name, func(req string) string {
		return `{"protocolVersion":1,"schemaVersion":1,"result":` + strings.TrimRight(req, "\n") + "}\n"
	})
}

// writeOpAwareFakeBackend is like writeFakeBackend, but replies with a
// different canned response depending on which op the incoming request
// names — needed to fake a backend that answers auth_status and
// capabilities differently in the same test.
func writeOpAwareFakeBackend(t *testing.T, name string, byOp map[string]string, defaultStdout string) {
	t.Helper()
	registerFake(t, name, fakeAnswers(t, "", byOp, defaultStdout))
}
