package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
)

// asProblem returns the problem an error is, if it is one.
func asProblem(err error) (*client.ProblemError, bool) {
	var pe *client.ProblemError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

type clientEvent = client.Event

func (s *state) serveCmd() *cobra.Command {
	var cfg, dir string
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon: the single writer of the event log, with its HTTP API",
		Long: `Run the daemon in the foreground (launchd supervises it). It reads the configuration file,
takes an exclusive lock on the data directory, replays the event log, and serves the API on
127.0.0.1 at the configured listen_port. SIGHUP reloads the configuration; SIGINT and SIGTERM
shut it down cleanly. Logs are JSON lines on stdout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if s.app.Serve == nil {
				return usagef("serve is not available in this build")
			}
			path := cfg
			if path == "" {
				path = s.app.Getenv("PG_TASK_FOCUS_CONFIG")
			}
			if path == "" {
				return usagef("serve needs --config (or $PG_TASK_FOCUS_CONFIG): the configuration file")
			}
			data := dir
			if data == "" {
				data = s.app.Getenv("PG_TASK_FOCUS_DATA_DIR")
			}
			if data == "" {
				data = store.DefaultDir(s.app.Getenv)
			}
			if data == "" {
				return usagef("serve needs --data-dir, $PG_TASK_FOCUS_DATA_DIR, or $XDG_DATA_HOME or $HOME to find the data directory")
			}
			if err := s.app.Serve(cmd.Context(), ServeParams{ConfigPath: path, DataDir: data}); err != nil {
				return &exitError{code: ExitStartFailed, msg: err.Error()}
			}
			return nil
		},
	}
	c.Flags().StringVar(&cfg, "config", "", "the configuration file (default $PG_TASK_FOCUS_CONFIG)")
	c.Flags().StringVar(&dir, "data-dir", "", "the data directory (default $PG_TASK_FOCUS_DATA_DIR, else $XDG_DATA_HOME/pg-task-focus or ~/.local/share/pg-task-focus)")
	return c
}

// CheckOutput is the JSON of `check`.
type CheckOutput struct {
	OK       bool          `json:"ok"`
	Path     string        `json:"path"`
	SizeB    int64         `json:"size_bytes"`
	Lines    int           `json:"lines"`
	Batches  int           `json:"batches"`
	Recovery CheckRecovery `json:"recovery"`
	Problem  string        `json:"problem,omitempty"`
	Line     int           `json:"line,omitempty"`
	Invalid  *CheckInvalid `json:"invalid,omitempty"`
}

// CheckRecovery is what the next start would do to the end of the log.
type CheckRecovery struct {
	TornTail           bool  `json:"torn_tail"`
	UncommittedBatches int   `json:"uncommitted_batches"`
	TruncatedBytes     int64 `json:"truncated_bytes"`
}

// CheckInvalid is the finding of replaying a log whose timeline is impossible.
type CheckInvalid struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Events  []string `json:"events"`
}

func (s *state) checkCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "check [LOG]",
		Short: "Verify an event log offline, without the daemon and without changing anything",
		Long: `Read the log (the data directory or the events.jsonl file; default the data directory)
and replay its committed events. It takes no lock, so it works while the daemon runs. Exit 0
means a daemon would start on the log; exit 8 means it would refuse, and the output says why,
with the line number.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := dir
			if len(args) == 1 {
				path = args[0]
			}
			if path == "" {
				path = s.app.Getenv("PG_TASK_FOCUS_DATA_DIR")
			}
			if path == "" {
				path = store.DefaultDir(s.app.Getenv)
			}
			if path == "" {
				return usagef("check needs a log: name it, or set $PG_TASK_FOCUS_DATA_DIR, $XDG_DATA_HOME or $HOME")
			}
			rep, err := engine.Verify(path)
			if err != nil {
				return err
			}
			out := checkOutput(rep)
			raw, _ := json.Marshal(out)
			if err := s.out(raw, func(w io.Writer) error { renderCheck(w, out); return nil }); err != nil {
				return err
			}
			if !out.OK {
				return &exitError{code: ExitCheckFailed, msg: "the log would not start a daemon"}
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "data-dir", "", "the data directory or log file to check")
	return c
}

func checkOutput(rep engine.VerifyReport) CheckOutput {
	out := CheckOutput{
		OK: rep.OK(), Path: rep.Check.Path, SizeB: rep.Check.Size, Lines: rep.Check.Lines, Batches: rep.Check.Batches,
		Recovery: CheckRecovery{
			TornTail: rep.Check.Recovery.TornTail, UncommittedBatches: rep.Check.Recovery.UncommittedBatches,
			TruncatedBytes: rep.Check.Recovery.TruncatedBytes,
		},
	}
	var ce *store.CorruptError
	if rep.Check.Problem != nil {
		out.Problem = rep.Check.Problem.Error()
		if errors.As(rep.Check.Problem, &ce) {
			out.Line = ce.Line
		}
	}
	if rep.Invalid != nil {
		inv := &CheckInvalid{Code: string(rep.Invalid.Code), Message: rep.Invalid.Message, Events: []string{}}
		for _, id := range rep.Invalid.Events {
			inv.Events = append(inv.Events, string(id))
		}
		out.Invalid = inv
		out.Problem = rep.Invalid.Message
	}
	return out
}

func renderCheck(w io.Writer, o CheckOutput) {
	fmt.Fprintf(w, "%s: %d committed line(s) in %d batch(es), %d bytes\n", o.Path, o.Lines, o.Batches, o.SizeB)
	if o.Recovery.TornTail || o.Recovery.UncommittedBatches > 0 {
		fmt.Fprintf(w, "The next start would recover the end of the log: torn tail %v, uncommitted batches %d, %d bytes cut (copied to a sidecar first).\n",
			o.Recovery.TornTail, o.Recovery.UncommittedBatches, o.Recovery.TruncatedBytes)
	}
	if o.OK {
		fmt.Fprintln(w, "OK: a daemon would start on this log.")
		return
	}
	if o.Line > 0 {
		fmt.Fprintf(w, "PROBLEM at line %d: %s\n", o.Line, o.Problem)
	} else {
		fmt.Fprintf(w, "PROBLEM: %s\n", o.Problem)
	}
	if o.Invalid != nil {
		fmt.Fprintf(w, "The timeline is impossible (%s).\n", o.Invalid.Code)
	}
}

// ConfigCheckOutput is the JSON of `config check`.
type ConfigCheckOutput struct {
	Valid    bool            `json:"valid"`
	Digest   string          `json:"digest,omitempty"`
	Problems []ConfigProblem `json:"problems"`
}

// ConfigProblem is one thing wrong with a configuration file.
type ConfigProblem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (s *state) configCmd() *cobra.Command {
	root := &cobra.Command{Use: "config", Short: "Work with the configuration file"}
	var file string
	check := &cobra.Command{
		Use:   "check [FILE]",
		Short: "Validate a configuration file and list every problem with its path",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := file
			if len(args) == 1 {
				path = args[0]
			}
			if path == "" {
				path = s.app.Getenv("PG_TASK_FOCUS_CONFIG")
			}
			if path == "" {
				return usagef("config check needs a file: name it, or set $PG_TASK_FOCUS_CONFIG")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out := ConfigCheckOutput{Problems: []ConfigProblem{}}
			parsed, perr := config.Parse(raw)
			if perr == nil {
				out.Valid, out.Digest = true, parsed.Digest()
			} else {
				var ve *config.ValidationError
				if !errors.As(perr, &ve) {
					return perr
				}
				for _, p := range ve.Problems {
					out.Problems = append(out.Problems, ConfigProblem{Path: p.Path, Message: p.Message})
				}
			}
			b, _ := json.Marshal(out)
			if err := s.out(b, func(w io.Writer) error {
				if out.Valid {
					fmt.Fprintf(w, "OK: the configuration is valid (digest %s).\n", out.Digest)
					return nil
				}
				for _, p := range out.Problems {
					where := p.Path
					if where == "" {
						where = "(document)"
					}
					fmt.Fprintf(w, "%s: %s\n", where, p.Message)
				}
				return nil
			}); err != nil {
				return err
			}
			if !out.Valid {
				return &exitError{code: ExitCheckFailed, msg: "the configuration is not valid"}
			}
			return nil
		},
	}
	check.Flags().StringVar(&file, "file", "", "the configuration file")
	root.AddCommand(check)
	return root
}
