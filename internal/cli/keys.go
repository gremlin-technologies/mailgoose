package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// apiKey is the API's ApiKey.
type apiKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	Hint       string     `json:"hint"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (a *App) keysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Create, list and revoke API keys",
		Long: `API keys let scripts and AI agents use mailgoose. A full-scope key can do
everything; a draft-scope key can do everything except launch campaigns,
change billing and manage keys, which suits an agent that drafts for you.
These commands need a full-scope key.`,
	}

	var name, scope string
	create := calls("createApiKey", &cobra.Command{
		Use:   "create",
		Short: "Make an API key and print its secret, which is shown only once",
		Example: `  mailgoose keys create --name "agent on my laptop" --scope draft
  mailgoose keys create --name ci --scope full --json | jq -r .secret`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return needFlag("name", "A name for the key")
			}
			if scope == "" {
				return needFlag("scope", "A scope (full or draft)")
			}
			if scope != "full" && scope != "draft" {
				return usageError("--scope must be full or draft.", "Use draft for an agent that shouldn't launch campaigns, change billing or manage keys.")
			}
			var out struct {
				Key    apiKey `json:"key"`
				Secret string `json:"secret"`
			}
			body := map[string]string{"name": name, "scope": scope}
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/api-keys", body, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Created %s key %q (%s).\n\n  %s\n\nThis is the only time the secret is shown. Store it somewhere safe.\n",
				out.Key.Scope, out.Key.Name, out.Key.ID, out.Secret)
			return nil
		},
	})
	create.Flags().StringVar(&name, "name", "", "what the key is for (required)")
	create.Flags().StringVar(&scope, "scope", "", "full or draft (required)")

	var limit int
	var cursor string
	list := calls("listApiKeys", &cobra.Command{
		Use:   "list",
		Short: "List the account's active API keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			path := "/v1/api-keys"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page struct {
				Data       []apiKey `json:"data"`
				NextCursor *string  `json:"next_cursor"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No API keys. Make one with `mailgoose keys create`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSCOPE\tKEY\tCREATED\tLAST USED")
			for _, k := range page.Data {
				used := "never"
				if k.LastUsedAt != nil {
					used = k.LastUsedAt.Local().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Scope, k.Hint, k.CreatedAt.Local().Format("2006-01-02 15:04"), used)
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore keys: mailgoose keys list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().IntVar(&limit, "limit", 0, "keys per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")

	var yes bool
	revoke := calls("revokeApiKey", &cobra.Command{
		Use:     "revoke <key-id>",
		Short:   "Revoke an API key; it stops working on its next request",
		Example: `  mailgoose keys revoke 3f2a9c1e-8b7d-4e6f-a5c4-1d2e3f4a5b6c --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return needFlag("yes", "Confirmation (revoking can't be undone)")
			}
			if err := a.call(cmd.Context(), http.MethodDelete, "/v1/api-keys/"+url.PathEscape(args[0]), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"id": args[0], "revoked": true})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Revoked %s.\n", args[0])
			return nil
		},
	})
	revoke.Flags().BoolVar(&yes, "yes", false, "confirm; revoking can't be undone")

	cmd.AddCommand(create, list, revoke)
	return cmd
}
