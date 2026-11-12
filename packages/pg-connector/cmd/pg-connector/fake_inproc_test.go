package main

import (
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// In-process fake backends. A backend invocation is a scriptout.Call whose
// Name is the backend's registered name; a fake registered under that name
// answers it with no process, no script file and no PATH edit. A Call for any
// other name still runs for real (ExecRunner), so a test that needs a real
// script (argv-aware or file-driven fakes) keeps using installExec.
//
// The shell fakes this replaces matched the raw request line with a `case`
// pattern on `"op":"<op>"`; fakeAnswers keeps exactly that substring match.

// fakeHandler answers one request line (the JSON the umbrella wrote on stdin).
type fakeHandler func(req string) (stdout string)

var (
	fakeMu       sync.Mutex
	fakeHandlers = map[string]fakeHandler{}
)

// registerFake answers backend name with h until the test ends.
func registerFake(t *testing.T, name string, h fakeHandler) {
	t.Helper()
	fakeMu.Lock()
	prev, had := fakeHandlers[name]
	fakeHandlers[name] = h
	fakeMu.Unlock()
	t.Cleanup(func() {
		fakeMu.Lock()
		defer fakeMu.Unlock()
		if had {
			fakeHandlers[name] = prev
		} else {
			delete(fakeHandlers, name)
		}
	})
}

// appendLine appends line and a newline to path (the request log a test reads).
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Errorf("open request log: %v", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Errorf("write request log: %v", err)
	}
}

// fakeAnswers is the handler for a backend that answers byOp[op] when the
// request names that op (substring match on `"op":"<op>"`, ops tried in
// sorted order) and fallback otherwise. When logPath is not empty every
// request line is appended to it first.
func fakeAnswers(t *testing.T, logPath string, byOp map[string]string, fallback string) fakeHandler {
	ops := make([]string, 0, len(byOp))
	for op := range byOp {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return func(req string) string {
		req = strings.TrimRight(req, "\n")
		if logPath != "" {
			appendLine(t, logPath, req)
		}
		for _, op := range ops {
			if strings.Contains(req, `"op":"`+op+`"`) {
				return byOp[op] + "\n"
			}
		}
		if fallback == "" {
			return ""
		}
		return fallback + "\n"
	}
}
