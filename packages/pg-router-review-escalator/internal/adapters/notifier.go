package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-router-review-escalator/internal/escalate"
)

// ExecNotifier is an escalate.Notifier that execs the operator's push command.
// The command is configuration (the operator's existing channel); this tool
// names no channel, service or host.
//
// Argv[0] is the program and the rest are its arguments. The placeholders
// {title}, {body}, {url} and {key} are replaced inside each argv element, and
// the same four values are also exported to the child as
// ESCALATION_TITLE, ESCALATION_BODY, ESCALATION_URL and ESCALATION_KEY. The
// text is untrusted tool output: it only ever travels as an argv element or an
// environment value, never through a shell this tool starts. A configured
// command that is itself a shell wrapper SHOULD read the environment values
// instead of expanding the placeholders into script text.
type ExecNotifier struct {
	Runner Runner
	Argv   []string
}

// Notify implements escalate.Notifier. Any failure to deliver, including a
// non-zero exit of the command, is an error.
func (n ExecNotifier) Notify(ctx context.Context, x escalate.Notification) error {
	if len(n.Argv) == 0 {
		return errors.New("no notify command is configured")
	}
	repl := strings.NewReplacer("{title}", x.Title, "{body}", x.Body, "{url}", x.URL, "{key}", x.Key)
	args := make([]string, 0, len(n.Argv)-1)
	for _, a := range n.Argv[1:] {
		args = append(args, repl.Replace(a))
	}
	env := []string{
		"ESCALATION_TITLE=" + x.Title,
		"ESCALATION_BODY=" + x.Body,
		"ESCALATION_URL=" + x.URL,
		"ESCALATION_KEY=" + x.Key,
	}
	res, err := n.Runner.Run(ctx, n.Argv[0], args, nil, env)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		return fmt.Errorf("%s exited %d: %s", n.Argv[0], res.ExitCode, msg)
	}
	return nil
}
