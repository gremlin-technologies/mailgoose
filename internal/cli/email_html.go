package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// htmlIssue is one problem the API found in an html email's markup.
type htmlIssue struct {
	Line    int    `json:"line"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a *App) emailImportHTMLCmd() *cobra.Command {
	var version int
	cmd := callsAll([]string{"importEmailHtml", "getEmail"}, &cobra.Command{
		Use:   "import-html <email-id> <file.html>",
		Short: "Replace an html email's markup with a file",
		Long: `Replaces the markup of an html email (one created with --kind html) with the
file, whole; - reads standard input. The email's settings stay as they are.

The markup is checked first, and any error stops the import: malformed
markup, <script>, <form>, frames, embedded objects, external stylesheets,
inline event handlers such as onclick, javascript: addresses, no
{{unsubscribe_url}}, or a file over 102 KB. Each problem is listed with its
line. CSS that some email clients drop (flexbox, position and the like) is a
warning: the import goes ahead and lists them.

When the email is sent, it gets the account's postal address in a footer at
the bottom unless the markup already shows it.

Without --version the markup replaces the email as it is now.`,
		Example: `  mailgoose email create --name "Spring menu" --kind html
  mailgoose email import-html 3f1c... ./spring.html`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			markup, err := a.readHTMLFile(args[1])
			if err != nil {
				return err
			}
			v := version
			if v == 0 {
				if v, err = a.currentVersion(cmd.Context(), id); err != nil {
					return err
				}
			}
			var out struct {
				Email    email       `json:"email"`
				Warnings []htmlIssue `json:"warnings"`
			}
			body := map[string]any{"version": v, "html": markup}
			if err := a.call(cmd.Context(), http.MethodPut, emailPath(id)+"/html", body, &out, true); err != nil {
				return listHTMLIssues(err)
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Imported %s of HTML into %q (%s), version %d.\n",
				sizeOf(len(markup)), out.Email.Name, out.Email.ID, out.Email.Version)
			if n := len(out.Warnings); n > 0 {
				fmt.Fprintf(a.Stdout, "\n%d %s: some email clients will show it differently.\n", n, plural(n, "warning", "warnings"))
				for _, w := range out.Warnings {
					fmt.Fprintf(a.Stdout, "  line %d: %s\n", w.Line, w.Message)
				}
			}
			return nil
		},
	})
	cmd.Flags().IntVar(&version, "version", 0, "the version you read; the import fails if the email changed since")
	return cmd
}

// readHTMLFile reads the markup: a path, or - for standard input.
func (a *App) readHTMLFile(name string) (string, error) {
	var rd io.Reader
	if name == "-" {
		rd = a.Stdin
	} else {
		f, err := os.Open(name) //nolint:gosec // the file the user named
		if err != nil {
			return "", usageError("Can't read "+name+": "+err.Error(), "Pass an HTML file, or - to read standard input.")
		}
		defer func() { _ = f.Close() }()
		rd = f
	}
	raw, err := io.ReadAll(io.LimitReader(rd, maxDocumentFile+1))
	if err != nil {
		return "", usageError("Can't read "+name+": "+err.Error(), "")
	}
	if len(raw) > maxDocumentFile {
		return "", usageError(name+" is over 1 MB; an email can be at most 102 KB.", "")
	}
	if !utf8.Valid(raw) {
		return "", usageError(name+" isn't UTF-8 text.", "Save the file as UTF-8 and try again.")
	}
	return string(raw), nil
}

// listHTMLIssues puts every problem in an invalid_html error's message,
// one per line, so the person sees them all, not just the first.
func listHTMLIssues(err error) error {
	var e *Error
	if !errors.As(err, &e) || e.Code != "invalid_html" {
		return err
	}
	raw, _ := e.Details["issues"].([]any)
	if len(raw) == 0 {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The HTML has %d %s, so nothing was imported:", len(raw), plural(len(raw), "problem", "problems"))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		line, _ := m["line"].(float64)
		msg, _ := m["message"].(string)
		fmt.Fprintf(&b, "\n  line %d: %s", int(line), msg)
	}
	e.Message = b.String()
	e.Hint = "Fix each line and run the import again."
	return e
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func sizeOf(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
