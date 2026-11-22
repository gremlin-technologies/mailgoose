package cli

import (
	_ "embed"

	"github.com/spf13/cobra"
)

// AgentGuide is the Markdown guide `mailgoose docs` prints, for AI agents
// working through the CLI (MG-55). docs/api/agent-guide.md is generated
// from it (make docs).
//
//go:embed agentguide.md
var AgentGuide string

func (a *App) docsCmd() *cobra.Command {
	return local(&cobra.Command{
		Use:   "docs",
		Short: "Print the guide for AI agents, in Markdown",
		Long: `Prints a Markdown guide for AI agents driving mailgoose: signing in with an
API key, building an email from blocks, checking it, sending a test, the
launch confirmation the person has to say yes to, and moving a person over
from Mailchimp. It needs no API key and makes no request.

With --json it prints {"format": "markdown", "guide": "..."}.`,
		Example: `  mailgoose docs
  mailgoose docs > mailgoose-guide.md`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if a.jsonOut {
				a.printJSON(map[string]string{"format": "markdown", "guide": AgentGuide})
				return nil
			}
			_, err := a.Stdout.Write([]byte(AgentGuide))
			return err
		},
	})
}
