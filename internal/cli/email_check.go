package cli

import (
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// checkIssue is one finding of the email check.
type checkIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	URL     string `json:"url,omitempty"`
}

type checkResult struct {
	OK       bool         `json:"ok"`
	Errors   []checkIssue `json:"errors"`
	Warnings []checkIssue `json:"warnings"`
}

func (a *App) emailCheckCmd() *cobra.Command {
	return calls("checkEmail", &cobra.Command{
		Use:   "check <email-id>",
		Short: "Check an email for problems before sending it",
		Long: `Looks the email over and lists what would stop it going (errors) and what
some readers would trip on (warnings).

Errors: no subject, no From address, a From that isn't on a verified sending
domain, no postal address on the account, an empty email, a merge tag that
can't be filled, no unsubscribe link, a link that isn't a full address or
leads to a missing page, a link on Google's phishing or malware lists, an
email over 102 KB, or markup that isn't allowed.

Warnings: http:// links, links that time out or answer with an error, links
that weren't checked or couldn't be looked up on the phishing and malware
lists, images without alt text, an email close to the size limit, and CSS
some clients drop.

Each link is fetched once, with a few seconds for each and 10 seconds in
all; links to private addresses or to ports other than 80 and 443 aren't
fetched. Links are fetched for at most 10 checks a minute.

The exit code is 1 when there are errors and 0 when there are only warnings
or nothing, so a script or an agent can check before it sends.`,
		Example: `  mailgoose email check 3f1c...
  mailgoose email check 3f1c... --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			var res checkResult
			if err := a.call(cmd.Context(), http.MethodPost, emailPath(id)+"/check", nil, &res, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(res)
			} else {
				printCheck(a.Stdout, res)
			}
			if n := len(res.Errors); n > 0 {
				return &Error{Exit: ExitError, Code: "email_has_errors",
					Message: fmt.Sprintf("The email has %d %s, so it can't be sent yet.", n, plural(n, "error", "errors")),
					Hint:    "Fix each error, then run `mailgoose email check` again."}
			}
			return nil
		},
	})
}

func printCheck(w io.Writer, res checkResult) {
	if len(res.Errors) == 0 && len(res.Warnings) == 0 {
		fmt.Fprintln(w, "No problems found.")
		return
	}
	list := func(title string, issues []checkIssue) {
		if len(issues) == 0 {
			return
		}
		fmt.Fprintf(w, "%s (%d):\n", title, len(issues))
		for _, i := range issues {
			where := ""
			switch {
			case i.Path != "":
				where = i.Path + ": "
			case i.Line > 0:
				where = fmt.Sprintf("line %d: ", i.Line)
			}
			fmt.Fprintf(w, "  %s%s [%s]\n", where, i.Message, i.Code)
		}
	}
	list("Errors", res.Errors)
	list("Warnings", res.Warnings)
}
