package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
)

// attendCandidates returns the live sessions waiting on the human: needs_input
// always, plus idle when includeDone. Dead rows (no live tmux pane) are dropped
// so the picker never selects a target runAttach can't attach to. Order is
// preserved from the store (last_activity DESC).
func attendCandidates(rows []store.Session, includeDone bool, liveFn func(socket, target string) bool, socket string) []store.Session {
	var out []store.Session
	for _, r := range rows {
		match := r.State == store.NeedsInput || (includeDone && r.State == store.Idle)
		if match && liveFn(socket, r.TmuxSession) {
			out = append(out, r)
		}
	}
	return out
}

func runAttend(args []string) int {
	fs := flag.NewFlagSet("attend", flag.ExitOnError)
	includeDone := fs.Bool("include-done", false, "also offer idle sessions")
	_ = parseInterspersed(fs, args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()
	rows, err := st.List(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "list:", err)
		return 1
	}

	cands := attendCandidates(rows, *includeDone, tmux.HasSession, cfg.Tmux.Socket)
	switch len(cands) {
	case 0:
		fmt.Println("no sessions waiting on input")
		return 0
	case 1:
		return runAttach([]string{cands[0].ExternalID})
	default:
		externalID, ok := realPicker().pickCandidate(cands)
		if !ok {
			return 0
		}
		return runAttach([]string{externalID})
	}
}

// picker bundles the environment-sensitive dependencies pickCandidate needs, so
// the three-way branch and the numbered-index parse are unit-testable without a
// real TTY, without fzf on PATH, and without touching os.Stdin. Matches the
// explicit-dependency idiom used by attendCandidates (liveFn) and handleHook
// (stdin io.Reader).
type picker struct {
	isTerminal func() bool                                // replaces the direct stdinIsTerminal() call
	hasFzf     func() bool                                // replaces the direct exec.LookPath("fzf") probe
	pickFzfFn  func(cands []store.Session) (string, bool) // replaces the direct pickFzf call (kept exec'd subprocess out of test scope)
	in         io.Reader                                  // replaces the direct os.Stdin read in pickNumbered
	out        io.Writer                                  // user-facing prompts/listing (was os.Stderr)
}

// realPicker wires the production picker: real TTY check, real PATH probe, real
// pickFzf, real stdin, and stderr for prompts (preserving the current behavior,
// which writes the listing and the pick> prompt to stderr).
func realPicker() picker {
	return picker{
		isTerminal: stdinIsTerminal,
		hasFzf:     func() bool { _, err := exec.LookPath("fzf"); return err == nil },
		pickFzfFn:  pickFzf,
		in:         os.Stdin,
		out:        os.Stderr,
	}
}

// pickCandidate prompts the user to choose one waiting session, using the
// injected picker environment. Uses fzf when present, else a numbered stdin
// prompt. When stdin is not an interactive terminal it cannot prompt: it lists
// the names and returns ("", false), preserving the pre-picker scriptable
// behavior.
func (p picker) pickCandidate(cands []store.Session) (string, bool) {
	if !p.isTerminal() {
		fmt.Fprintln(p.out, "sessions waiting on input (no TTY to pick):")
		for _, c := range cands {
			fmt.Fprintln(p.out, " ", c.ExternalID)
		}
		return "", false
	}
	if p.hasFzf() {
		return p.pickFzfFn(cands)
	}
	return p.pickNumbered(cands)
}

// candidateLine renders a tab-delimited row. The FIRST column is the external_id
// (the addressing key pickFzf/pickNumbered return); the name is shown next as the
// human label (ADR 0015).
func candidateLine(c store.Session) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s", c.ExternalID, c.Name, c.State, c.CWD,
		time.Unix(c.LastActivityAt, 0).Format("2006-01-02 15:04:05"))
}

// pickFzf pipes one tab-delimited line per candidate to fzf and returns the
// selected external_id (the first column). fzf draws on /dev/tty and returns the
// choice on stdout.
func pickFzf(cands []store.Session) (string, bool) {
	var in strings.Builder
	for _, c := range cands {
		in.WriteString(candidateLine(c))
		in.WriteByte('\n')
	}
	cmd := exec.Command("fzf", "--delimiter", "\t", "--with-nth", "1,2,3,4,5", "--prompt", "attend> ")
	cmd.Stdin = strings.NewReader(in.String())
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", false // non-zero exit = user cancelled (Esc) or no match
	}
	sel := strings.TrimSpace(string(out))
	if sel == "" {
		return "", false
	}
	externalID, _, _ := strings.Cut(sel, "\t")
	return externalID, externalID != ""
}

// pickNumbered prints a numbered list to p.out and reads one line (the index)
// from p.in.
func (p picker) pickNumbered(cands []store.Session) (string, bool) {
	fmt.Fprintln(p.out, "sessions waiting on input:")
	for i, c := range cands {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, candidateLine(c))
	}
	fmt.Fprint(p.out, "pick> ")
	r := bufio.NewReader(p.in)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	idx, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || idx < 1 || idx > len(cands) {
		return "", false
	}
	return cands[idx-1].ExternalID, true
}

// stdinIsTerminal reports whether stdin is an interactive char device (so we
// can prompt). Stdlib check; avoids an x/term dependency.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
