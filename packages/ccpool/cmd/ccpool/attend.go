package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
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
// always, plus done when includeDone. Dead rows (no live tmux pane) are dropped
// so the picker never selects a target runAttach can't attach to. Order is
// preserved from the store (last_activity DESC).
func attendCandidates(rows []store.Session, includeDone bool, liveFn func(socket, target string) bool, socket string) []store.Session {
	var out []store.Session
	for _, r := range rows {
		match := r.State == store.NeedsInput || (includeDone && r.State == store.Done)
		if match && liveFn(socket, r.TmuxSession) {
			out = append(out, r)
		}
	}
	return out
}

func runAttend(args []string) int {
	fs := flag.NewFlagSet("attend", flag.ExitOnError)
	includeDone := fs.Bool("include-done", false, "also offer done sessions")
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
		return runAttach([]string{cands[0].Name})
	default:
		name, ok := pickCandidate(cands)
		if !ok {
			return 0
		}
		return runAttach([]string{name})
	}
}

// pickCandidate prompts the user to choose one waiting session. Uses fzf when
// present, else a numbered stdin prompt. When stdin is not an interactive
// terminal it cannot prompt: it lists the names and returns ("", false),
// preserving the pre-picker scriptable behavior.
func pickCandidate(cands []store.Session) (string, bool) {
	if !stdinIsTerminal() {
		fmt.Fprintln(os.Stderr, "sessions waiting on input (no TTY to pick):")
		for _, c := range cands {
			fmt.Println(" ", c.Name)
		}
		return "", false
	}
	if _, err := exec.LookPath("fzf"); err == nil {
		return pickFzf(cands)
	}
	return pickNumbered(cands)
}

func candidateLine(c store.Session) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s", c.Name, c.State, c.CWD,
		time.Unix(c.LastActivityAt, 0).Format("2006-01-02 15:04:05"))
}

// pickFzf pipes one tab-delimited line per candidate to fzf and returns the
// selected name. fzf draws on /dev/tty and returns the choice on stdout.
func pickFzf(cands []store.Session) (string, bool) {
	var in strings.Builder
	for _, c := range cands {
		in.WriteString(candidateLine(c))
		in.WriteByte('\n')
	}
	cmd := exec.Command("fzf", "--delimiter", "\t", "--with-nth", "1,2,3,4", "--prompt", "attend> ")
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
	name, _, _ := strings.Cut(sel, "\t")
	return name, name != ""
}

// pickNumbered prints a numbered list and reads one line (the index) from stdin.
func pickNumbered(cands []store.Session) (string, bool) {
	fmt.Fprintln(os.Stderr, "sessions waiting on input:")
	for i, c := range cands {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, candidateLine(c))
	}
	fmt.Fprint(os.Stderr, "pick> ")
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	idx, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || idx < 1 || idx > len(cands) {
		return "", false
	}
	return cands[idx-1].Name, true
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
