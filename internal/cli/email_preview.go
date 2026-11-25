package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

func (a *App) emailPreviewCmd() *cobra.Command {
	var width, out, contact string
	var open bool
	cmd := callsAll([]string{"previewEmail", "listContacts"}, &cobra.Command{
		Use:   "preview <email-id>",
		Short: "Render an email as it will look",
		Long: `Renders the email as it will be sent, filled in with sample data (Alex Rivera,
alex@example.com), and writes the HTML to standard output, or to --out. The
sample person has no contact field values, so each field's merge tag, such
as {{company|your team}}, shows its fallback or nothing. --contact fills
the tags from one of your contacts instead (an id or an address): their
name, address and field values.

--width mobile shows the email in a 375 pixel frame, so its phone layout
applies. --open opens the result in your browser when you're at a terminal;
without --out it writes a file in your temporary directory first.

Until the account has a postal address, the footer shows a placeholder where
it goes.`,
		Example: `  mailgoose email preview 3f1c... --out spring.html
  mailgoose email preview 3f1c... --width mobile --open
  mailgoose email preview 3f1c... --contact pat@example.com --out pat.html`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			if width != "desktop" && width != "mobile" {
				return usageError("--width must be desktop or mobile.", "")
			}
			q := url.Values{"width": {width}}
			if contact != "" {
				cid, err := a.contactID(cmd.Context(), contact)
				if err != nil {
					return err
				}
				q.Set("contact_id", cid)
			}
			var page bytes.Buffer
			path := emailPath(id) + "/preview?" + q.Encode()
			if err := a.download(cmd.Context(), path, &page); err != nil {
				return err
			}
			if out == "" && !open {
				_, err := a.Stdout.Write(page.Bytes())
				return err
			}
			file := out
			if file == "" {
				f, err := os.CreateTemp("", "mailgoose-preview-*.html")
				if err != nil {
					return err
				}
				file = f.Name()
				_ = f.Close()
			}
			if err := os.WriteFile(file, page.Bytes(), 0o600); err != nil {
				return usageError("Can't write "+file+": "+err.Error(), "")
			}
			if abs, err := filepath.Abs(file); err == nil {
				file = abs
			}
			opened := false
			if open {
				if !a.IsTerminal() {
					fmt.Fprintln(a.Stderr, "Not opening a browser: this isn't a terminal.")
				} else if err := a.OpenFile(cmd.Context(), file); err != nil {
					fmt.Fprintln(a.Stderr, "Couldn't open a browser:", err)
				} else {
					opened = true
				}
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"file": file, "width": width, "opened": opened})
				return nil
			}
			verb := "Wrote"
			if opened {
				verb = "Opened"
			}
			fmt.Fprintf(a.Stdout, "%s the %s preview: %s\n", verb, width, file)
			return nil
		},
	})
	cmd.Flags().StringVar(&width, "width", "desktop", "desktop or mobile")
	cmd.Flags().StringVar(&out, "out", "", "write the HTML to this file instead of standard output")
	cmd.Flags().BoolVar(&open, "open", false, "open the preview in your browser (only at a terminal)")
	cmd.Flags().StringVar(&contact, "contact", "", "fill the merge tags from this contact (an id or an address) instead of the sample")
	return cmd
}

// openInBrowser opens a local file with the system's default handler. The
// handler keeps running after the command ends.
func openInBrowser(ctx context.Context, path string) error {
	// The browser outlives this command, so cancelling ctx mustn't kill it.
	ctx = context.WithoutCancel(ctx)
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.CommandContext(ctx, "open", path) //nolint:gosec // a file this command wrote
	case "windows":
		c = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", path) //nolint:gosec // a file this command wrote
	default:
		c = exec.CommandContext(ctx, "xdg-open", path) //nolint:gosec // a file this command wrote
	}
	return c.Start()
}
