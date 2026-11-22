package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// segmentConditions is the API's SegmentConditions.
type segmentConditions struct {
	TagIDs            []string `json:"tag_ids,omitempty"`
	Statuses          []string `json:"statuses,omitempty"`
	Opened            []string `json:"opened,omitempty"`
	NotOpened         []string `json:"not_opened,omitempty"`
	Clicked           []string `json:"clicked,omitempty"`
	NotClicked        []string `json:"not_clicked,omitempty"`
	NeverOpened       bool     `json:"never_opened,omitempty"`
	NeverClicked      bool     `json:"never_clicked,omitempty"`
	OpenedWithinDays  int      `json:"opened_within_days,omitempty"`
	ClickedWithinDays int      `json:"clicked_within_days,omitempty"`
}

type segmentProblem struct {
	Condition string `json:"condition"`
	ID        string `json:"id"`
	Code      string `json:"code"`
}

// segment is the API's Segment.
type segment struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Conditions segmentConditions `json:"conditions"`
	Valid      bool              `json:"valid"`
	Problems   []segmentProblem  `json:"problems"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type segmentPage struct {
	Data       []segment `json:"data"`
	NextCursor *string   `json:"next_cursor"`
}

// segmentID turns a segment's name or id into its id, looking the name up.
func (a *App) segmentID(ctx context.Context, arg string) (string, error) {
	if domainIDPattern.MatchString(arg) {
		return arg, nil
	}
	var page segmentPage
	if err := a.call(ctx, http.MethodGet, "/v1/segments?"+url.Values{"name": {arg}}.Encode(), nil, &page, true); err != nil {
		return "", err
	}
	if len(page.Data) == 0 {
		return "", &Error{Exit: ExitNotFound, Code: "not_found", Message: fmt.Sprintf("This account has no segment %q.", arg),
			Hint: "`mailgoose segment list` shows the segments, and `mailgoose segment create` makes one."}
	}
	return page.Data[0].ID, nil
}

// conditionFlags are a segment's conditions as flags.
type conditionFlags struct {
	tags, statuses                         []string
	opened, notOpened, clicked, notClicked []string
	neverOpened, neverClicked              bool
	openedWithin, clickedWithin            int
}

func (f *conditionFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringArrayVar(&f.tags, "tag", nil, "contacts with this tag (name or id); repeat for more, and each must match")
	fl.StringArrayVar(&f.statuses, "status", nil, "contacts with this status ("+contactStatuses+"); repeat for any of several")
	fl.StringArrayVar(&f.opened, "opened", nil, "contacts who opened this campaign (id; repeat for more)")
	fl.StringArrayVar(&f.notOpened, "not-opened", nil, "contacts delivered this campaign who didn't open it (id; repeat for more)")
	fl.StringArrayVar(&f.clicked, "clicked", nil, "contacts who clicked in this campaign (id; repeat for more)")
	fl.StringArrayVar(&f.notClicked, "not-clicked", nil, "contacts delivered this campaign who didn't click it (id; repeat for more)")
	fl.BoolVar(&f.neverOpened, "never-opened", false, "contacts delivered a tracked campaign who opened none")
	fl.BoolVar(&f.neverClicked, "never-clicked", false, "contacts delivered a tracked campaign who clicked none")
	fl.IntVar(&f.openedWithin, "opened-within-days", 0, "contacts who opened any campaign in the last this many days (1 to 365)")
	fl.IntVar(&f.clickedWithin, "clicked-within-days", 0, "contacts who clicked any campaign in the last this many days (1 to 365)")
}

// given says whether any condition flag was set on cmd.
func (f *conditionFlags) given(cmd *cobra.Command) bool {
	for _, n := range []string{"tag", "status", "opened", "not-opened", "clicked", "not-clicked",
		"never-opened", "never-clicked", "opened-within-days", "clicked-within-days"} {
		if cmd.Flags().Changed(n) {
			return true
		}
	}
	return false
}

// conditions builds the API's conditions, looking tag names up as ids.
func (f *conditionFlags) conditions(ctx context.Context, a *App) (segmentConditions, error) {
	c := segmentConditions{Statuses: f.statuses, Opened: f.opened, NotOpened: f.notOpened, Clicked: f.clicked,
		NotClicked: f.notClicked, NeverOpened: f.neverOpened, NeverClicked: f.neverClicked,
		OpenedWithinDays: f.openedWithin, ClickedWithinDays: f.clickedWithin}
	for _, t := range f.tags {
		id, err := a.tagID(ctx, t)
		if err != nil {
			return c, err
		}
		c.TagIDs = append(c.TagIDs, id)
	}
	return c, nil
}

// describe is c in words, one condition a line.
func (c segmentConditions) describe() []string {
	var out []string
	for _, l := range []struct {
		label string
		ids   []string
	}{
		{"has tag", c.TagIDs}, {"opened", c.Opened}, {"didn't open", c.NotOpened},
		{"clicked", c.Clicked}, {"didn't click", c.NotClicked},
	} {
		for _, id := range l.ids {
			out = append(out, l.label+" "+id)
		}
	}
	if len(c.Statuses) > 0 {
		out = append(out, "status is "+strings.Join(c.Statuses, " or "))
	}
	if c.NeverOpened {
		out = append(out, "never opened a tracked campaign")
	}
	if c.NeverClicked {
		out = append(out, "never clicked a tracked campaign")
	}
	if c.OpenedWithinDays > 0 {
		out = append(out, fmt.Sprintf("opened in the last %d days", c.OpenedWithinDays))
	}
	if c.ClickedWithinDays > 0 {
		out = append(out, fmt.Sprintf("clicked in the last %d days", c.ClickedWithinDays))
	}
	return out
}

var problemWords = map[string]string{
	"tag_deleted":      "the tag was deleted",
	"campaign_deleted": "the campaign was deleted",
	"tracking_off":     "the campaign is set to send without tracking",
	"report_archived":  "the campaign's raw events were archived after 12 months",
}

func (a *App) printSegment(sg segment) {
	fmt.Fprintf(a.Stdout, "%s (%s)\n", sg.Name, sg.ID)
	fmt.Fprintln(a.Stdout, "Contacts who match all of:")
	for _, line := range sg.Conditions.describe() {
		fmt.Fprintln(a.Stdout, "  "+line)
	}
	if !sg.Valid {
		fmt.Fprintln(a.Stdout, "Invalid until its conditions are updated:")
		for _, p := range sg.Problems {
			fmt.Fprintf(a.Stdout, "  %s %s: %s\n", p.Condition, p.ID, problemWords[p.Code])
		}
	}
}

func (a *App) segmentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "segment",
		Aliases: []string{"segments"},
		Short:   "Save sets of contact conditions as segments",
		Long: `A segment is a named set of conditions on contacts: tags, statuses, and what
they did with past campaigns (the engagement filters of "mailgoose contacts
list"). A contact is in it when it matches every condition. It's worked out
each time it's used, so it stays current.

A condition naming a tag or campaign that's since been deleted, a campaign
set to send without tracking, or one whose events were archived after 12
months makes the segment invalid: "segment get" shows which condition, and
"segment contacts" refuses until "segment update" fixes it. A segment's name
or id works wherever a segment is asked for.`,
	}

	var limit int
	var cursor string
	list := calls("listSegments", &cobra.Command{
		Use:     "list",
		Short:   "List segments",
		Example: "  mailgoose segment list\n  mailgoose segment list --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			var page segmentPage
			if err := a.call(cmd.Context(), http.MethodGet, withQuery("/v1/segments", q), nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No segments. Make one with `mailgoose segment create <name> --tag <tag>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tCONDITIONS\tVALID")
			for _, sg := range page.Data {
				valid := "yes"
				if !sg.Valid {
					valid = "no"
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", sg.ID, sg.Name, len(sg.Conditions.describe()), valid)
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore segments: mailgoose segment list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().IntVar(&limit, "limit", 0, "segments per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")

	get := callsAll([]string{"getSegment", "listSegments"}, &cobra.Command{
		Use:     "get <segment>",
		Short:   "Show a segment's conditions and any problems",
		Example: "  mailgoose segment get engaged\n  mailgoose segment get engaged --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.segmentID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var sg segment
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/segments/"+url.PathEscape(id), nil, &sg, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(sg)
				return nil
			}
			a.printSegment(sg)
			return nil
		},
	})

	var createConds conditionFlags
	create := callsAll([]string{"createSegment", "listTags"}, &cobra.Command{
		Use:   "create <name>",
		Short: "Save a segment",
		Long: `Saves a segment of the contacts matching every condition given. Give at
least one. --tag may repeat, and a contact needs every tag; --status may
repeat, and a contact needs one of them. The engagement flags work as they do
on "mailgoose contacts list". An account keeps up to 100 segments.`,
		Example: `  mailgoose segment create engaged --status subscribed --opened-within-days 90
  mailgoose segment create "VIP non-clickers" --tag vip --not-clicked <campaign-id>
  mailgoose segment create cold --never-opened --status subscribed --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conds, err := createConds.conditions(cmd.Context(), a)
			if err != nil {
				return err
			}
			var sg segment
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/segments",
				map[string]any{"name": args[0], "conditions": conds}, &sg, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(sg)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Created segment %q (%s). See who's in it: mailgoose segment contacts %s\n", sg.Name, sg.ID, sg.ID)
			return nil
		},
	})
	createConds.register(create)

	var updateConds conditionFlags
	var newName string
	update := callsAll([]string{"updateSegment", "listSegments", "listTags"}, &cobra.Command{
		Use:   "update <segment>",
		Short: "Rename a segment or replace its conditions",
		Long: `Renames a segment with --name, or replaces its conditions: give any
condition flag and the new conditions are exactly the ones given, as on
"segment create". Without one, the conditions stay as they are.`,
		Example: `  mailgoose segment update engaged --name "Engaged (90 days)"
  mailgoose segment update engaged --status subscribed --opened-within-days 60`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if cmd.Flags().Changed("name") {
				body["name"] = newName
			}
			if updateConds.given(cmd) {
				conds, err := updateConds.conditions(cmd.Context(), a)
				if err != nil {
					return err
				}
				body["conditions"] = conds
			}
			if len(body) == 0 {
				return usageError("Say what to change.", "Pass --name, or condition flags such as --tag or --opened-within-days to replace the conditions.")
			}
			id, err := a.segmentID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var sg segment
			if err := a.call(cmd.Context(), http.MethodPatch, "/v1/segments/"+url.PathEscape(id), body, &sg, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(sg)
				return nil
			}
			fmt.Fprintln(a.Stdout, "Updated.")
			a.printSegment(sg)
			return nil
		},
	})
	update.Flags().StringVar(&newName, "name", "", "the segment's new name")
	updateConds.register(update)

	var yes bool
	del := callsAll([]string{"deleteSegment", "listSegments"}, &cobra.Command{
		Use:     "delete <segment>",
		Short:   "Delete a segment; its contacts stay as they are",
		Example: "  mailgoose segment delete engaged --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return needFlag("yes", "Confirmation (deleting can't be undone)")
			}
			id, err := a.segmentID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.call(cmd.Context(), http.MethodDelete, "/v1/segments/"+url.PathEscape(id), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"deleted": true, "id": id})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted segment %q.\n", args[0])
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm the delete")

	var memberLimit int
	var memberCursor string
	members := callsAll([]string{"listSegmentContacts", "listSegments"}, &cobra.Command{
		Use:   "contacts <segment>",
		Short: "List the contacts in a segment now",
		Long: `Lists the contacts matching the segment's conditions now, newest first, a
page at a time, with how many there are in all.`,
		Example: "  mailgoose segment contacts engaged\n  mailgoose segment contacts engaged --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.segmentID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			q := url.Values{}
			if memberLimit > 0 {
				q.Set("limit", strconv.Itoa(memberLimit))
			}
			if memberCursor != "" {
				q.Set("cursor", memberCursor)
			}
			var page struct {
				contactPage
				Count int `json:"count"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, withQuery("/v1/segments/"+url.PathEscape(id)+"/contacts", q), nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			noun := "contacts"
			if page.Count == 1 {
				noun = "contact"
			}
			fmt.Fprintf(a.Stdout, "%d %s in the segment.\n", page.Count, noun)
			if len(page.Data) == 0 {
				return nil
			}
			fmt.Fprintln(a.Stdout)
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tEMAIL\tNAME\tSTATUS\tTAGS\tADDED")
			for _, c := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Email, c.name(), c.Status, c.tagNames(), c.CreatedAt.Local().Format("2006-01-02 15:04"))
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore contacts: mailgoose segment contacts %s --cursor %s\n", args[0], *page.NextCursor)
			}
			return nil
		},
	})
	members.Flags().IntVar(&memberLimit, "limit", 0, "contacts per page (1 to 100, default 50)")
	members.Flags().StringVar(&memberCursor, "cursor", "", "next_cursor from the previous page")

	cmd.AddCommand(list, get, create, update, del, members)
	return cmd
}
