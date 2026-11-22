package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

// contactFilterFlags are the filters `contacts list`, `contacts count` and
// `export` share: the contact's own fields, and what it did with past
// campaigns (MG-113).
type contactFilterFlags struct {
	email, name                            string
	statuses, tags                         []string
	opened, notOpened, clicked, notClicked []string
	neverOpened, neverClicked              bool
	openedWithin, clickedWithin            int
}

func (f *contactFilterFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.email, "email", "", "only addresses containing this")
	fl.StringVar(&f.name, "name", "", "only names containing this")
	fl.StringArrayVar(&f.statuses, "status", nil, "only this status ("+contactStatuses+"); repeat for any of several")
	fl.StringArrayVar(&f.tags, "tag", nil, "only contacts with this tag (name or id); repeat for contacts with every one")
	fl.StringArrayVar(&f.opened, "opened", nil, "only contacts who opened this campaign (id; repeat for more)")
	fl.StringArrayVar(&f.notOpened, "not-opened", nil, "only contacts delivered this campaign who didn't open it (id; repeat for more)")
	fl.StringArrayVar(&f.clicked, "clicked", nil, "only contacts who clicked in this campaign (id; repeat for more)")
	fl.StringArrayVar(&f.notClicked, "not-clicked", nil, "only contacts delivered this campaign who didn't click it (id; repeat for more)")
	fl.BoolVar(&f.neverOpened, "never-opened", false, "only contacts delivered a tracked campaign who opened none")
	fl.BoolVar(&f.neverClicked, "never-clicked", false, "only contacts delivered a tracked campaign who clicked none")
	fl.IntVar(&f.openedWithin, "opened-within-days", 0, "only contacts who opened any campaign in the last this many days (1 to 365)")
	fl.IntVar(&f.clickedWithin, "clicked-within-days", 0, "only contacts who clicked any campaign in the last this many days (1 to 365)")
}

// query is the filters as the API's query parameters; a tag name is looked
// up as its id.
func (f *contactFilterFlags) query(ctx context.Context, a *App) (url.Values, error) {
	q := url.Values{}
	for k, v := range map[string]string{"email": f.email, "name": f.name} {
		if v != "" {
			q.Set(k, v)
		}
	}
	for _, st := range f.statuses {
		q.Add("status", st)
	}
	for _, tag := range f.tags {
		id, err := a.tagID(ctx, tag)
		if err != nil {
			return nil, err
		}
		q.Add("tag_id", id)
	}
	for k, ids := range map[string][]string{"opened": f.opened, "not_opened": f.notOpened, "clicked": f.clicked, "not_clicked": f.notClicked} {
		for _, id := range ids {
			q.Add(k, id)
		}
	}
	if f.neverOpened {
		q.Set("never_opened", "true")
	}
	if f.neverClicked {
		q.Set("never_clicked", "true")
	}
	if f.openedWithin != 0 {
		q.Set("opened_within_days", strconv.Itoa(f.openedWithin))
	}
	if f.clickedWithin != 0 {
		q.Set("clicked_within_days", strconv.Itoa(f.clickedWithin))
	}
	return q, nil
}

// withQuery is path with q, if any.
func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// contactsCountCmd is `mailgoose contacts count`.
func (a *App) contactsCountCmd() *cobra.Command {
	var filters contactFilterFlags
	cmd := callsAll([]string{"countContacts", "listTags"}, &cobra.Command{
		Use:   "count",
		Short: "Count the contacts that match the filters",
		Long: `Counts the account's contacts: every one, or those matching the filters of
"mailgoose contacts list", engagement ones included.`,
		Example: `  mailgoose contacts count --status subscribed
  mailgoose contacts count --never-opened --status subscribed
  mailgoose contacts count --clicked <campaign-id> --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := filters.query(cmd.Context(), a)
			if err != nil {
				return err
			}
			var out struct {
				Count int `json:"count"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, withQuery("/v1/contacts/count", q), nil, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			noun := "contacts match"
			if out.Count == 1 {
				noun = "contact matches"
			}
			fmt.Fprintf(a.Stdout, "%d %s.\n", out.Count, noun)
			return nil
		},
	})
	filters.register(cmd)
	return cmd
}
