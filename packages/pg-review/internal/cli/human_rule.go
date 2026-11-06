package cli

import "github.com/spf13/cobra"

func init() { registerCommand(groupHuman, newHumanRuleCmd) }

func newHumanRuleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rule <id>... <ruling>",
		Short: "Rule on one or more shown items: adopted, deferred, rejected or reopen",
		Args:  cobra.MinimumNArgs(2),
		RunE:  func(*cobra.Command, []string) error { return notImplemented() },
	}
	f := cmd.Flags()
	f.String("note", "", "a note to store with the ruling")
	f.String("reconsider-when", "", "when to reconsider (for a deferred item)")
	f.String("useful", "", "whether the item was useful: yes or no")
	return cmd
}
