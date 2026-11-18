package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// dnsRecord is the API's DnsRecord.
type dnsRecord struct {
	Purpose  string   `json:"purpose"`
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Value    string   `json:"value"`
	Priority *int     `json:"priority"`
	Required bool     `json:"required"`
	Status   string   `json:"status"`
	Found    []string `json:"found"`
	Detail   *string  `json:"detail"`
}

// domain is the API's Domain.
type domain struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Status         string      `json:"status"`
	MailFromDomain string      `json:"mail_from_domain"`
	SESStatus      *string     `json:"ses_status"`
	Records        []dnsRecord `json:"records"`
	RecordsChanged *bool       `json:"records_changed,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	LastCheckedAt  *time.Time  `json:"last_checked_at"`
	CheckUntil     time.Time   `json:"check_until"`
	VerifiedAt     *time.Time  `json:"verified_at"`
	FailedAt       *time.Time  `json:"failed_at"`
}

type domainPage struct {
	Data       []domain `json:"data"`
	NextCursor *string  `json:"next_cursor"`
}

var domainIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// domainID turns a domain's name or id into its id, looking the name up.
func (a *App) domainID(ctx context.Context, arg string) (string, error) {
	if domainIDPattern.MatchString(arg) {
		return arg, nil
	}
	var page domainPage
	if err := a.call(ctx, http.MethodGet, "/v1/domains?name="+url.QueryEscape(strings.ToLower(strings.TrimSuffix(arg, "."))), nil, &page, true); err != nil {
		return "", err
	}
	if len(page.Data) == 0 {
		return "", &Error{Exit: ExitNotFound, Code: "not_found", Message: fmt.Sprintf("This account has no domain %s.", arg),
			Hint: "`mailgoose domain list` shows the account's domains, and `mailgoose domain add` adds one."}
	}
	return page.Data[0].ID, nil
}

func (a *App) printDomain(w io.Writer, d domain) {
	fmt.Fprintf(w, "%s (%s)\nStatus: %s\n", d.Name, d.ID, d.Status)
	if d.LastCheckedAt != nil {
		fmt.Fprintf(w, "Last checked: %s\n", d.LastCheckedAt.Local().Format("2006-01-02 15:04"))
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tTYPE\tNAME\tVALUE")
	for _, r := range d.Records {
		value := r.Value
		if r.Priority != nil {
			value = strconv.Itoa(*r.Priority) + " " + value
		}
		status := r.Status
		if !r.Required {
			status += " (suggested)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", status, r.Type, r.Name, value)
	}
	_ = tw.Flush()
	for _, r := range d.Records {
		if r.Detail != nil {
			fmt.Fprintf(w, "\n%s %s: %s", r.Type, r.Name, *r.Detail)
		}
	}
	if d.RecordsChanged != nil && *d.RecordsChanged {
		fmt.Fprintln(w, "\n\nThe domain had failed, so it has new DKIM records. Replace the old CNAMEs with the ones above.")
	}
	switch d.Status {
	case "pending":
		fmt.Fprintf(w, "\nAdd the records above at your DNS provider. mailgoose checks them every 5 minutes until %s;\n`mailgoose domain verify %s` checks now.\n",
			d.CheckUntil.Local().Format("2006-01-02 15:04"), d.Name)
	case "failed":
		fmt.Fprintf(w, "\nThe records weren't found within 72 hours. Fix them, then run `mailgoose domain verify %s`.\n", d.Name)
	case "verified":
		fmt.Fprintf(w, "\nYou can send from any address at %s.\n", d.Name)
	}
}

func (a *App) domainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "domain",
		Aliases: []string{"domains"},
		Short:   "Add and verify the domains you send from",
		Long: `Campaigns go out from an address at a domain you own. Add the domain, put the
DNS records mailgoose prints at your DNS provider, and mailgoose checks them
every 5 minutes for 72 hours. A domain or its id works wherever one is asked for.`,
	}

	add := calls("createDomain", &cobra.Command{
		Use:     "add <domain>",
		Short:   "Add a sending domain and print the DNS records to put in place",
		Example: `  mailgoose domain add example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var d domain
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/domains", map[string]string{"name": args[0]}, &d, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(d)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Added %s.\n\n", d.Name)
			a.printDomain(a.Stdout, d)
			return nil
		},
	})

	var name, cursor string
	var limit int
	list := calls("listDomains", &cobra.Command{
		Use:   "list",
		Short: "List the account's sending domains",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if name != "" {
				q.Set("name", name)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			path := "/v1/domains"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page domainPage
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No sending domains. Add one with `mailgoose domain add <domain>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tDOMAIN\tSTATUS\tADDED")
			for _, d := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Status, d.CreatedAt.Local().Format("2006-01-02 15:04"))
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore domains: mailgoose domain list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().StringVar(&name, "name", "", "only this domain")
	list.Flags().IntVar(&limit, "limit", 0, "domains per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")

	get := callsAll([]string{"getDomain", "listDomains"}, &cobra.Command{
		Use:     "get <domain>",
		Short:   "Show a domain, its DNS records and what the last check found",
		Example: `  mailgoose domain get example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.domainID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var d domain
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/domains/"+url.PathEscape(id), nil, &d, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(d)
				return nil
			}
			a.printDomain(a.Stdout, d)
			return nil
		},
	})

	verify := callsAll([]string{"verifyDomain", "listDomains"}, &cobra.Command{
		Use:   "verify <domain>",
		Short: "Look the domain's DNS records up now and report each one",
		Long: `Looks up each record now and reports it found, missing or wrong. The exit
code is 0 once the domain is verified and 1 while it isn't.`,
		Example: `  mailgoose domain verify example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.domainID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var d domain
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/domains/"+url.PathEscape(id)+"/verify", nil, &d, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(d)
			} else {
				a.printDomain(a.Stdout, d)
			}
			if d.Status != "verified" {
				return &Error{Exit: ExitError, Code: "domain_not_verified",
					Message: fmt.Sprintf("%s isn't verified yet (%s).", d.Name, d.Status),
					Hint:    "Fix the records marked missing or wrong; DNS changes can take a while to show."}
			}
			return nil
		},
	})

	var yes bool
	del := callsAll([]string{"deleteDomain", "listDomains"}, &cobra.Command{
		Use:     "delete <domain>",
		Short:   "Remove a sending domain; mail can't go out from it afterwards",
		Example: `  mailgoose domain delete example.com --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return needFlag("yes", "Confirmation (adding the domain again means new DKIM records)")
			}
			id, err := a.domainID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.call(cmd.Context(), http.MethodDelete, "/v1/domains/"+url.PathEscape(id), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"id": id, "deleted": true})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted %s.\n", args[0])
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm the delete")

	cmd.AddCommand(add, list, get, verify, del)
	return cmd
}
