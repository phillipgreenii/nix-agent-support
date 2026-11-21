package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/pg-gwm/internal/manager"
)

// schemaVersion is the version written into every JSON output.
const schemaVersion = 1

// maxListed bounds the files or commits a dirty refusal lists in text.
const maxListed = 20

func defaultIsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// stdoutIsTerminal reports whether standard output is a terminal; a writer
// that is not a file never is.
func (a *app) stdoutIsTerminal() bool {
	f, ok := a.stdout.(*os.File)
	return ok && a.isTerminal(f)
}

// withSchema returns v's JSON object with schema_version first.
func withSchema(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(b, []byte("{")) {
		return nil, fmt.Errorf("output is not a JSON object")
	}
	head := fmt.Sprintf(`{"schema_version":%d`, schemaVersion)
	if string(b) == "{}" {
		return []byte(head + "}"), nil
	}
	return append([]byte(head+","), b[1:]...), nil
}

// emit prints a success: one JSON object with schema_version, or the text
// form. It returns the exit code.
func (a *app) emit(format string, v any) int {
	if format == "json" {
		if recs, ok := v.([]manager.Record); ok { // a list is a bare array
			b, err := json.Marshal(recs)
			if err != nil {
				return a.fail(err)
			}
			fmt.Fprintln(a.stdout, string(b))
			return 0
		}
		b, err := withSchema(v)
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintln(a.stdout, string(b))
		return 0
	}
	switch rec := v.(type) {
	case []manager.Record:
		writeListText(a.stdout, rec)
	case manager.Record:
		writeRecordText(a.stdout, rec)
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintln(a.stdout, string(b))
	}
	return 0
}

func orDash(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// writeListText prints one table row per record.
func writeListText(w io.Writer, recs []manager.Record) {
	fmt.Fprintf(w, "%-12s %-14s %-20s %-24s %-24s %s\n", "ID", "STATUS", "NAME", "BRANCH", "EXPIRES", "PATH")
	for _, r := range recs {
		id := r.ID
		if id == "" {
			id = "-"
		}
		fmt.Fprintf(w, "%-12s %-14s %-20s %-24s %-24s %s\n",
			id, r.Status, orDash(r.Name), orDash(r.Branch), orDash(r.LeaseExpiration), r.WorktreePath)
	}
}

func writeRecordText(w io.Writer, r manager.Record) {
	rows := [][2]string{
		{"path", r.WorktreePath},
		{"name", orDash(r.Name)},
		{"status", string(r.Status)},
		{"branch", orDash(r.Branch)},
		{"owner", orDash(r.Owner)},
		{"expires", orDash(r.LeaseExpiration)},
		{"id", r.ID},
		{"lease", orDash(r.LeaseID)},
		{"head", r.Head},
	}
	if r.Renewed != nil {
		rows = append(rows, [2]string{"renewed", fmt.Sprint(*r.Renewed)})
	}
	for _, row := range rows {
		fmt.Fprintf(w, "%-8s %s\n", row[0]+":", row[1])
	}
	if len(r.Metadata) > 0 {
		fmt.Fprintf(w, "%-8s %v\n", "metadata:", r.Metadata)
	}
}

// emitErr reports err and returns its exit code. Text goes to standard error
// (a summary line, the hint, and for a dirty refusal the files and commits);
// JSON goes to standard output as the error envelope. With printPath standard
// output stays empty and the text form is used in every format, so
// cd "$(pg-gwm lease ... --print=path)" fails instead of receiving JSON. Every
// refusal is counted; an interruption is not one.
func (a *app) emitErr(format string, printPath bool, err error) int {
	e := manager.AsError(err)
	if e.Name != "interrupted" && a.counters.Path != "" && a.label != "" {
		a.counters.Refusal(context.Background(), a.label, e.Name)
	}
	if format == "json" && !printPath {
		var hint any
		if e.Hint != "" {
			hint = e.Hint
		}
		details := e.Details
		if details == nil {
			details = map[string]any{}
		}
		env := map[string]any{"error": map[string]any{
			"name": e.Name, "code": e.Code, "message": e.Message, "hint": hint, "details": details,
		}}
		b, merr := json.Marshal(env)
		if merr != nil {
			fmt.Fprintf(a.stderr, "pg-gwm: %v\n", merr)
			return e.Code
		}
		out, _ := withSchema(json.RawMessage(b))
		fmt.Fprintln(a.stdout, string(out))
		return e.Code
	}
	fmt.Fprintf(a.stderr, "pg-gwm: %s\n", e.Message)
	if e.Hint != "" {
		fmt.Fprintf(a.stderr, "hint: %s\n", e.Hint)
	}
	if e.Name == "dirty" {
		for _, key := range []string{"files", "commits"} {
			writeList(a.stderr, key, e.Details[key])
		}
		writeList(a.stderr, "resolve with", e.Details["commands"])
	}
	return e.Code
}

func writeList(w io.Writer, title string, v any) {
	items, _ := v.([]string)
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s:\n", title)
	for i, it := range items {
		if i == maxListed {
			fmt.Fprintf(w, "    ... and %d more\n", len(items)-maxListed)
			break
		}
		fmt.Fprintf(w, "    %s\n", strings.TrimSpace(it))
	}
}

// warnf prints a warning to standard error, in every format.
func (a *app) warnf(format string, args ...any) {
	fmt.Fprintf(a.stderr, "pg-gwm: warning: "+format+"\n", args...)
}
