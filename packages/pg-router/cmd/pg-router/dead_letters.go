package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
)

// deadLetterView is one record of `dead-letters --json`, under the field names of
// the failed queue-log record.
type deadLetterView struct {
	EventID  string `json:"eventId"`
	Role     string `json:"role"`
	Class    string `json:"class"`
	Attempts int    `json:"attempts"`
	Reason   string `json:"reason"`
	At       string `json:"at"`
}

type deadLettersReply struct {
	Records []deadLetterView `json:"records"`
}

// runDeadLetters implements `pg-router dead-letters [--json]`: it lists the failed
// records the queue log still holds. It only reads the log file (no lock), so it
// works with the daemon running or not.
func runDeadLetters(args []string) int {
	fs, _, _ := gateFlags("dead-letters")
	asJSON := fs.Bool("json", false, "emit {\"records\":[...]}")
	pos, code, ok := parseGateFlags(fs, "dead-letters", args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "dead-letters: unexpected argument:", pos[0])
		return exitUsage
	}
	return deadLetters(os.Stdout, os.Stderr, filepath.Join(config.LogDir(), "queue.jsonl"), *asJSON)
}

// deadLetters is runDeadLetters' testable body.
func deadLetters(stdout, stderr io.Writer, path string, asJSON bool) int {
	recs, err := eventqueue.ReadDeadLettersFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "dead-letters: cannot read %s: %v\n", textsafe.Sanitize(path), err)
		return exitUnavailable
	}
	views := make([]deadLetterView, 0, len(recs))
	for _, r := range recs {
		views = append(views, deadLetterView{
			EventID: r.EventID, Role: r.Role, Class: r.Class,
			Attempts: r.Attempts, Reason: r.Reason, At: r.At.UTC().Format(time.RFC3339),
		})
	}
	if asJSON {
		b, err := json.Marshal(deadLettersReply{Records: views})
		if err != nil { // unreachable: plain scalars
			fmt.Fprintln(stderr, "dead-letters:", err)
			return exitGeneric
		}
		fmt.Fprintln(stdout, string(b))
		return exitOK
	}
	if len(views) == 0 {
		fmt.Fprintln(stdout, "no dead letters")
		return exitOK
	}
	for _, v := range views {
		fmt.Fprintf(stdout, "%s role=%s class=%s attempts=%d reason=%s at=%s\n",
			textsafe.Sanitize(v.EventID), textsafe.Sanitize(v.Role), v.Class, v.Attempts, v.Reason, v.At)
	}
	return exitOK
}
