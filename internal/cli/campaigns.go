package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// campaign is the API's Campaign.
type campaign struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	EmailID    *string `json:"email_id"`
	Recipients struct {
		All  bool `json:"all"`
		Tags []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tags"`
	} `json:"recipients"`
	RecipientCount *int       `json:"recipient_count,omitempty"`
	SendAt         *time.Time `json:"send_at"`
	Tracking       bool       `json:"tracking"`
	Status         string     `json:"status"`
	Version        int        `json:"version"`
	LastTest       *struct {
		At           time.Time `json:"at"`
		Version      int       `json:"version"`
		EmailVersion int       `json:"email_version"`
	} `json:"last_test"`
	Launch *struct {
		At              time.Time `json:"at"`
		RecipientCount  int       `json:"recipient_count"`
		CreditsReserved int       `json:"credits_reserved"`
	} `json:"launch"`
	Slice     *campaignSlice `json:"slice,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// campaignSlice is a new account's first slice (MG-62).
type campaignSlice struct {
	State     string     `json:"state"`
	Size      int        `json:"size"`
	ReleaseAt *time.Time `json:"release_at"`
}

// describe says where the slice stands, in loc.
func (s *campaignSlice) describe(loc *time.Location) string {
	when := func() string {
		if s.ReleaseAt == nil {
			return ""
		}
		return " until " + s.ReleaseAt.In(loc).Format(timeLayout)
	}
	n := fmt.Sprintf("%d %s", s.Size, plural(s.Size, "recipient", "recipients"))
	switch s.State {
	case "sending_slice":
		return "sending the first " + n + "; the rest waits for their bounces and complaints"
	case "slice_hold":
		return "first " + n + " sent; the rest waits" + when() + ", then goes if they're within limits"
	case "held":
		return "first " + n + " went over the early-warning limits; the rest is held" + when() + ", then checked again"
	case "released":
		return "first " + n + " were within limits; the rest was released"
	case "stopped":
		return "first " + n + " went over the bounce or complaint limits; the rest wasn't sent and its credits are back"
	case "cancelled":
		return "first " + n + " sent; the campaign was cancelled, so the rest wasn't sent and its credits are back"
	}
	return s.State + ", " + n
}

type campaignPage struct {
	Data       []campaign `json:"data"`
	NextCursor *string    `json:"next_cursor"`
}

func campaignIDArg(arg string) (string, error) {
	if !domainIDPattern.MatchString(arg) {
		return "", usageError(fmt.Sprintf("%q isn't a campaign id.", arg), "`mailgoose campaign list` shows the account's campaigns and their ids.")
	}
	return arg, nil
}

func campaignPath(id string) string { return "/v1/campaigns/" + url.PathEscape(id) }

// sendAtLayouts are the local-time forms --send-at takes besides RFC 3339.
var sendAtLayouts = []string{"2006-01-02 15:04", "2006-01-02T15:04"}

// timeLayout is how the CLI shows campaign times, in the account's zone.
const timeLayout = "2006-01-02 15:04 MST"

// accountZone is the account's time zone (`mailgoose account update
// --time-zone`), read once. Send times are shown in it, and a date and time
// without an offset is read in it. If the account can't be read, the
// computer's own zone is used.
func (a *App) accountZone(ctx context.Context) *time.Location {
	if a.zone != nil {
		return a.zone
	}
	a.zone = time.Local
	var acct struct {
		TimeZone string `json:"time_zone"`
	}
	if err := a.call(ctx, http.MethodGet, "/v1/account", nil, &acct, true); err == nil && acct.TimeZone != "" {
		if loc, err := time.LoadLocation(acct.TimeZone); err == nil {
			a.zone = loc
		}
	}
	return a.zone
}

// loc is the account's zone once read, else the computer's.
func (a *App) loc() *time.Location {
	if a.zone != nil {
		return a.zone
	}
	return time.Local
}

// parseSendAt reads a send time flag: "now" for when launched (nil), an
// RFC 3339 time, or a date and time in the account's zone.
func (a *App) parseSendAt(ctx context.Context, flag, v string) (*time.Time, error) {
	if strings.EqualFold(v, "now") {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, nil
	}
	for _, layout := range sendAtLayouts {
		if _, err := time.Parse(layout, v); err == nil {
			loc := a.accountZone(ctx)
			t, _ := time.ParseInLocation(layout, v, loc)
			return &t, nil
		}
	}
	return nil, usageError(fmt.Sprintf("--%s %q isn't a time.", flag, v),
		`Use now, a time like 2026-12-01T09:00:00Z, or "2026-12-01 09:00" in the account's time zone.`)
}

// recipientFlags pick who a campaign goes to.
type recipientFlags struct {
	all  bool
	tags []string
}

func (f *recipientFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.all, "all", false, "send to every subscribed contact")
	cmd.Flags().StringArrayVar(&f.tags, "tag", nil, "send to subscribed contacts with this tag (name or id); repeat for any of several")
}

// body is the RecipientsInput for the flags, or nil when neither is given.
func (a *App) recipientsBody(ctx context.Context, f *recipientFlags) (map[string]any, error) {
	switch {
	case f.all && len(f.tags) > 0:
		return nil, usageError("Pass --all or --tag, not both.", "")
	case f.all:
		return map[string]any{"all": true}, nil
	case len(f.tags) == 0:
		return nil, nil
	}
	ids := make([]string, 0, len(f.tags))
	for _, t := range f.tags {
		id, err := a.tagID(ctx, t)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return map[string]any{"tag_ids": ids}, nil
}

func (c campaign) audience() string {
	if c.Recipients.All {
		return "every subscribed contact"
	}
	if len(c.Recipients.Tags) == 0 {
		return "nobody (its tags were deleted; pick recipients with --all or --tag)"
	}
	names := make([]string, len(c.Recipients.Tags))
	for i, t := range c.Recipients.Tags {
		names[i] = t.Name
	}
	return "subscribed contacts tagged " + strings.Join(names, " or ")
}

func (c campaign) sendTime(loc *time.Location) string {
	if c.SendAt == nil {
		return "when launched"
	}
	return c.SendAt.In(loc).Format(timeLayout)
}

func tracking(on bool) string {
	if on {
		return "opens and clicks"
	}
	return "off"
}

func (a *App) printCampaign(ctx context.Context, c campaign, verb string) {
	if a.jsonOut {
		a.printJSON(c)
		return
	}
	a.accountZone(ctx)
	if verb != "" {
		fmt.Fprintf(a.Stdout, "%s campaign %q (%s), version %d.\n", verb, c.Name, c.ID, c.Version)
	}
	email := "none: its email was deleted; pick one with --email"
	if c.EmailID != nil {
		email = *c.EmailID
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	if verb == "" {
		fmt.Fprintf(tw, "Name:\t%s\n", c.Name)
		fmt.Fprintf(tw, "ID:\t%s\n", c.ID)
		fmt.Fprintf(tw, "Status:\t%s\n", c.Status)
		fmt.Fprintf(tw, "Version:\t%d\n", c.Version)
	}
	fmt.Fprintf(tw, "Email:\t%s\n", email)
	fmt.Fprintf(tw, "To:\t%s\n", c.audience())
	if c.RecipientCount != nil {
		fmt.Fprintf(tw, "Recipients now:\t%d\n", *c.RecipientCount)
	}
	fmt.Fprintf(tw, "Send:\t%s\n", c.sendTime(a.loc()))
	fmt.Fprintf(tw, "Tracking:\t%s\n", tracking(c.Tracking))
	if verb == "" {
		test := "none yet (`mailgoose campaign test " + c.ID + "`)"
		if c.LastTest != nil {
			test = fmt.Sprintf("%s, of campaign version %d", c.LastTest.At.In(a.loc()).Format(timeLayout), c.LastTest.Version)
		}
		fmt.Fprintf(tw, "Last test:\t%s\n", test)
		if c.Launch != nil {
			fmt.Fprintf(tw, "Launched:\t%s, to %d %s, %d %s reserved\n", c.Launch.At.In(a.loc()).Format(timeLayout),
				c.Launch.RecipientCount, plural(c.Launch.RecipientCount, "contact", "contacts"),
				c.Launch.CreditsReserved, plural(c.Launch.CreditsReserved, "credit", "credits"))
		}
		if c.Slice != nil {
			fmt.Fprintf(tw, "First slice:\t%s\n", c.Slice.describe(a.loc()))
		}
	}
	_ = tw.Flush()
}

func (a *App) campaignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "campaign",
		Aliases: []string{"campaigns"},
		Short:   "Set up campaigns: an email, who gets it and when",
		Long: `A campaign sends one email to every subscribed contact (--all) or to the
subscribed contacts with any of some tags (--tag), when it's launched or at a
set time (--send-at). Unconfirmed, unsubscribed, bounced and complained
contacts never get it.

A campaign starts as a draft. Its status then moves through scheduled,
sending and sent; cancelled, paused and failed are the other ends. Only
drafts can be changed, tested or deleted: a launched campaign was confirmed
as it is.`,
	}

	var limit int
	var cursor, status string
	list := calls("listCampaigns", &cobra.Command{
		Use: "list",
		Example: `  mailgoose campaign list
  mailgoose campaign list --status scheduled --json`,
		Short: "List campaigns, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if status != "" {
				q.Set("status", status)
			}
			path := "/v1/campaigns"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page campaignPage
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No campaigns. Make one with `mailgoose campaign create --name <name> --email <email-id> --all`.")
				return nil
			}
			a.accountZone(cmd.Context())
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tSEND\tUPDATED")
			for _, c := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.ID, c.Name, c.Status, c.sendTime(a.loc()), c.UpdatedAt.In(a.loc()).Format("2006-01-02 15:04"))
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore campaigns: mailgoose campaign list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().IntVar(&limit, "limit", 0, "campaigns per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")
	list.Flags().StringVar(&status, "status", "", "only this status: draft, scheduled, sending, sent, cancelled, paused or failed")

	var name, emailID, sendAt string
	var track bool
	var cr recipientFlags
	create := calls("createCampaign", &cobra.Command{
		Use:   "create",
		Short: "Make a draft campaign",
		Long: `Makes a draft campaign. It needs a name, an email (--email) and recipients:
--all, or one or more --tag. Without --send-at it goes out when launched.
Nothing is sent until it's launched.`,
		Example: `  mailgoose campaign create --name "Spring sale" --email 3f1c... --all
  mailgoose campaign create --name "VIP preview" --email 3f1c... --tag VIP --tag Wholesale --send-at "2026-12-01 09:00"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return needFlag("name", "A name")
			}
			if emailID == "" {
				return needFlag("email", "The email to send (`mailgoose email list` shows them)")
			}
			recipients, err := a.recipientsBody(cmd.Context(), &cr)
			if err != nil {
				return err
			}
			if recipients == nil {
				return usageError("Say who gets it.", "Pass --all for every subscribed contact, or --tag for contacts with a tag.")
			}
			body := map[string]any{"name": name, "email_id": emailID, "recipients": recipients}
			if cmd.Flags().Changed("tracking") {
				body["tracking"] = track
			}
			if sendAt != "" {
				at, err := a.parseSendAt(cmd.Context(), "send-at", sendAt)
				if err != nil {
					return err
				}
				body["send_at"] = at
			}
			var c campaign
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/campaigns", body, &c, true); err != nil {
				return err
			}
			a.printCampaign(cmd.Context(), c, "Created")
			return nil
		},
	})
	create.Flags().StringVar(&name, "name", "", "the campaign's name, for you (required)")
	create.Flags().StringVar(&emailID, "email", "", "the id of the email to send (required)")
	create.Flags().StringVar(&sendAt, "send-at", "", `when to send: a time, or now for when launched (the default)`)
	create.Flags().BoolVar(&track, "tracking", true, "count opens and clicks; --tracking=false sends it untracked")
	cr.register(create)

	get := calls("getCampaign", &cobra.Command{
		Use:     "get <campaign-id>",
		Example: `  mailgoose campaign get 3f1c... --json`,
		Short:   "Show a campaign and how many it would reach now",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			var c campaign
			if err := a.call(cmd.Context(), http.MethodGet, campaignPath(id), nil, &c, true); err != nil {
				return err
			}
			a.printCampaign(cmd.Context(), c, "")
			return nil
		},
	})

	var newName, newEmail, newSendAt string
	var newTrack bool
	var ur recipientFlags
	var version int
	update := calls("updateCampaign", &cobra.Command{
		Use:   "update <campaign-id>",
		Short: "Change a draft campaign",
		Long: `Changes the parts given and leaves the rest. --all or --tag replaces the
recipients. --send-at now sends it when launched. With --version the change
is refused if the campaign changed since you read it.`,
		Example: `  mailgoose campaign update 3f1c... --tag VIP --send-at 2026-12-01T09:00:00Z
  mailgoose campaign update 3f1c... --name "Spring sale, final" --version 3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			flags := cmd.Flags()
			body := map[string]any{}
			if flags.Changed("name") {
				body["name"] = newName
			}
			if flags.Changed("email") {
				body["email_id"] = newEmail
			}
			if flags.Changed("tracking") {
				body["tracking"] = newTrack
			}
			if flags.Changed("send-at") {
				at, err := a.parseSendAt(cmd.Context(), "send-at", newSendAt)
				if err != nil {
					return err
				}
				body["send_at"] = at
			}
			recipients, err := a.recipientsBody(cmd.Context(), &ur)
			if err != nil {
				return err
			}
			if recipients != nil {
				body["recipients"] = recipients
			}
			if len(body) == 0 {
				return usageError("Nothing to change.", "Pass --name, --email, --all, --tag, --send-at or --tracking.")
			}
			if flags.Changed("version") {
				body["version"] = version
			}
			var c campaign
			if err := a.call(cmd.Context(), http.MethodPatch, campaignPath(id), body, &c, true); err != nil {
				return err
			}
			a.printCampaign(cmd.Context(), c, "Updated")
			return nil
		},
	})
	update.Flags().StringVar(&newName, "name", "", "a new name")
	update.Flags().StringVar(&newEmail, "email", "", "the id of another email to send")
	update.Flags().StringVar(&newSendAt, "send-at", "", "when to send: a time, or now for when launched")
	update.Flags().BoolVar(&newTrack, "tracking", true, "count opens and clicks (--tracking=false turns it off)")
	update.Flags().IntVar(&version, "version", 0, "the version you read; the change fails if the campaign changed since")
	ur.register(update)

	var yes bool
	del := calls("deleteCampaign", &cobra.Command{
		Use:     "delete <campaign-id>",
		Short:   "Delete a draft campaign",
		Example: `  mailgoose campaign delete 3f1c... --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			if !yes {
				return needFlag("yes", "Confirmation (deleting can't be undone)")
			}
			if err := a.call(cmd.Context(), http.MethodDelete, campaignPath(id), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"deleted": id})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted campaign %s.\n", id)
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm the delete")

	var cc recipientFlags
	count := calls("countCampaignRecipients", &cobra.Command{
		Use:   "count",
		Short: "Count who --all or --tag would reach now",
		Example: `  mailgoose campaign count --all
  mailgoose campaign count --tag VIP --tag Wholesale`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			recipients, err := a.recipientsBody(cmd.Context(), &cc)
			if err != nil {
				return err
			}
			if recipients == nil {
				return usageError("Say whom to count.", "Pass --all, or --tag for contacts with a tag.")
			}
			var out struct {
				RecipientCount int `json:"recipient_count"`
			}
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/campaigns/recipient-count", map[string]any{"recipients": recipients}, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "%d subscribed contacts.\n", out.RecipientCount)
			return nil
		},
	})
	cc.register(count)

	var to []string
	test := calls("testCampaign", &cobra.Command{
		Use:   "test <campaign-id>",
		Short: "Send a test of a campaign's email, to you or a colleague",
		Long: `Sends one copy of the campaign's email, with "[Test] " before the subject,
to you (the account owner), or with --to to up to 5 addresses: yours, or any
on a verified sending domain of the account. Merge tags are filled from the
contact with that address when there is one. No credits are used, and the
account can send 50 test emails in any 24 hours.

The email has to be ready to send: a From on a verified domain (or, where
the server has shared sending on, a verified sender address: see
` + "`mailgoose sender-addresses`" + `), a subject, the account's postal
address, and merge tags that can be filled in. If it isn't, nothing is sent
and each problem is listed with how to fix it.`,
		Example: `  mailgoose campaign test 3f1c...
  mailgoose campaign test 3f1c... --to sam@yourbakery.com --to you@yourbakery.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			var body any
			if len(to) > 0 {
				body = map[string]any{"to": to}
			}
			var out struct {
				Campaign     campaign   `json:"campaign"`
				To           []string   `json:"to"`
				SentToday    int        `json:"sent_today"`
				DailyLimit   int        `json:"daily_limit"`
				From         mailAddr   `json:"from"`
				ReplyTo      []mailAddr `json:"reply_to"`
				SharedDomain bool       `json:"shared_domain"`
				Sender       *string    `json:"sender"`
			}
			if err := a.call(cmd.Context(), http.MethodPost, campaignPath(id)+"/test", body, &out, true); err != nil {
				return listSendProblems(err, "The campaign can't be sent yet, so nothing was sent.")
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Sent a test of %q to %s.\n", out.Campaign.Name, strings.Join(out.To, ", "))
			fmt.Fprintf(a.Stdout, "%d of %d test emails used in the last 24 hours.\n", out.SentToday, out.DailyLimit)
			if out.SharedDomain && out.Sender != nil {
				replyTo := *out.Sender
				if len(out.ReplyTo) > 0 {
					replyTo = out.ReplyTo[0].Email
				}
				fmt.Fprintln(a.Stdout, sharedNudge(out.From.Email, replyTo, *out.Sender))
			}
			return nil
		},
	})
	test.Flags().StringArrayVar(&to, "to", nil, "send to this address instead of yours; repeat for up to 5")

	cmd.AddCommand(list, create, get, update, del, count, test, a.campaignLaunchCmd(), a.campaignCancelCmd(), a.campaignRescheduleCmd(),
		a.campaignReportCmd())
	return cmd
}

func (a *App) campaignCancelCmd() *cobra.Command {
	var yes bool
	cancel := calls("cancelCampaign", &cobra.Command{
		Use:   "cancel <campaign-id>",
		Short: "Cancel a scheduled campaign before it sends",
		Long: `Cancels a scheduled campaign before its send time. Nobody gets it, and
the credits reserved at launch come back. A new account's campaign whose
first slice is out and whose rest is waiting can be cancelled the same way:
the rest never goes, and its credits come back. Once it is sending it can't
be cancelled. Cancelling can't be undone: to send it after all, make a new
campaign.`,
		Example: `  mailgoose campaign cancel 3f1c... --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			if !yes {
				return needFlag("yes", "Confirmation (cancelling can't be undone)")
			}
			var c campaign
			if err := a.call(cmd.Context(), http.MethodPost, campaignPath(id)+"/cancel", nil, &c, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(c)
				return nil
			}
			if c.Slice != nil {
				fmt.Fprintf(a.Stdout, "Cancelled campaign %q (%s). The rest won't get it, and its reserved credits are back.\n", c.Name, c.ID)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Cancelled campaign %q (%s). Nobody gets it, and its reserved credits are back.\n", c.Name, c.ID)
			return nil
		},
	})
	cancel.Flags().BoolVar(&yes, "yes", false, "confirm the cancel")
	return cancel
}

func (a *App) campaignRescheduleCmd() *cobra.Command {
	var at string
	reschedule := calls("rescheduleCampaign", &cobra.Command{
		Use:   "reschedule <campaign-id> --at <time>",
		Short: "Move a scheduled campaign's send time",
		Long: `Moves a scheduled campaign to a new send time, before it starts
sending. The time is in the future and at most a year ahead: a time with
its offset, or a date and time in the account's time zone (see
` + "`mailgoose account get`" + `).`,
		Example: `  mailgoose campaign reschedule 3f1c... --at "2026-12-01 09:00"
  mailgoose campaign reschedule 3f1c... --at 2026-12-01T14:00:00Z`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			if at == "" {
				return needFlag("at", "The new send time")
			}
			t, err := a.parseSendAt(cmd.Context(), "at", at)
			if err != nil {
				return err
			}
			if t == nil {
				return usageError("A scheduled campaign needs a time; --at now isn't one.",
					"To send it now, cancel it and launch a copy without a send time.")
			}
			var c campaign
			body := map[string]any{"send_at": t.UTC().Format(time.RFC3339)}
			if err := a.call(cmd.Context(), http.MethodPost, campaignPath(id)+"/reschedule", body, &c, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(c)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Campaign %q now sends at %s.\n", c.Name, c.sendTime(a.accountZone(cmd.Context())))
			return nil
		},
	})
	reschedule.Flags().StringVar(&at, "at", "", "the new send time (required)")
	return reschedule
}

func (a *App) campaignLaunchCmd() *cobra.Command {
	var confirm string
	launch := calls("launchCampaign", &cobra.Command{
		Use:   "launch <campaign-id>",
		Short: "Launch a campaign, after a summary and a confirmation code",
		Long: `Launching takes two steps, so nothing goes out by accident.

Without --confirm, it checks the campaign can go (tested as it is now, an
email ready to send, credits for everyone it reaches) and prints a summary:
the subject, the From, how many contacts it goes to, when, and the credits it
uses. It also prints a confirmation code and exits with code 5. Nothing is
sent.

With --confirm and that code, it launches: the recipients are fixed, the
credits reserved, and the campaign goes out now or at its send time. The
code works for 15 minutes, for that campaign as it was summarized, and allows
5 wrong tries. If the campaign or its email changes, ask for a new summary.

An AI agent must show the summary to the person it works for and run the
command with --confirm only after they say yes in the chat.`,
		Example: `  mailgoose campaign launch 3f1c...
  mailgoose campaign launch 3f1c... --confirm K7QM-2PXD`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			var body any
			if cmd.Flags().Changed("confirm") {
				body = map[string]any{"confirm": confirm}
			}
			var out struct {
				Campaign        campaign `json:"campaign"`
				RecipientCount  int      `json:"recipient_count"`
				CreditsReserved int      `json:"credits_reserved"`
			}
			if err := a.call(cmd.Context(), http.MethodPost, campaignPath(id)+"/launch", body, &out, true); err != nil {
				return a.launchError(cmd.Context(), id, err)
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			when := "It's sending now."
			if out.Campaign.Status == "scheduled" {
				when = "It sends at " + out.Campaign.sendTime(a.accountZone(cmd.Context())) + "."
			}
			fmt.Fprintf(a.Stdout, "Launched %q to %d %s, with %d %s reserved. %s\n", out.Campaign.Name,
				out.RecipientCount, plural(out.RecipientCount, "contact", "contacts"),
				out.CreditsReserved, plural(out.CreditsReserved, "credit", "credits"), when)
			return nil
		},
	})
	launch.Flags().StringVar(&confirm, "confirm", "", "the confirmation code from the summary, once the person said yes")
	return launch
}

// launchError prints a launch summary, when that's the answer, and lists
// the problems of a campaign that can't launch.
func (a *App) launchError(ctx context.Context, id string, err error) error {
	var e *Error
	if !errors.As(err, &e) {
		return err
	}
	if e.Code != "confirmation_required" {
		return listSendProblems(err, "The campaign can't launch yet, so nothing was launched.")
	}
	code, _ := e.Details["code"].(string)
	sum, _ := e.Details["summary"].(map[string]any)
	if a.jsonOut || sum == nil || code == "" {
		return e
	}
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	num := func(k string) int { v, _ := sum[k].(float64); return int(v) }
	addr := func(v any) string {
		m, _ := v.(map[string]any)
		if n := str(m, "name"); n != "" {
			return fmt.Sprintf("%s <%s>", n, str(m, "email"))
		}
		return str(m, "email")
	}
	when := "as soon as it's launched"
	if t, err := time.Parse(time.RFC3339, str(sum, "send_at")); err == nil {
		when = t.In(a.accountZone(ctx)).Format(timeLayout)
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "Campaign:\t%s\n", str(sum, "name"))
	fmt.Fprintf(tw, "Subject:\t%s\n", str(sum, "subject"))
	fmt.Fprintf(tw, "From:\t%s\n", addr(sum["from"]))
	if rt, _ := sum["reply_to"].([]any); len(rt) > 0 {
		list := make([]string, len(rt))
		for i, r := range rt {
			list[i] = addr(r)
		}
		fmt.Fprintf(tw, "Reply-To:\t%s\n", strings.Join(list, ", "))
	}
	n := num("recipient_count")
	fmt.Fprintf(tw, "To:\t%d %s\n", n, plural(n, "contact", "contacts"))
	fmt.Fprintf(tw, "Sends:\t%s\n", when)
	fmt.Fprintf(tw, "Credits:\t%d of %d\n", num("credits"), num("balance"))
	_ = tw.Flush()
	if shared, _ := sum["shared_domain"].(bool); shared {
		if sender, _ := sum["sender"].(string); sender != "" {
			replyTo := sender
			if rt, _ := sum["reply_to"].([]any); len(rt) > 0 {
				m, _ := rt[0].(map[string]any)
				replyTo = str(m, "email")
			}
			m, _ := sum["from"].(map[string]any)
			fmt.Fprintln(a.Stdout, sharedNudge(str(m, "email"), replyTo, sender))
		}
	}
	if ws, _ := sum["warnings"].([]any); len(ws) > 0 {
		fmt.Fprintf(a.Stdout, "%d %s:\n", len(ws), plural(len(ws), "warning", "warnings"))
		for _, w := range ws {
			m, _ := w.(map[string]any)
			fmt.Fprintf(a.Stdout, "  - %s\n", str(m, "message"))
		}
	}
	fmt.Fprintf(a.Stdout, "Confirmation code: %s\n", code)
	return &Error{Exit: ExitNeedsConfirmation, Code: e.Code, Details: e.Details,
		Message: "Nothing was launched. The summary above needs confirming.",
		Hint: "Show the summary to the person. Only if they say yes, run `mailgoose campaign launch " + id +
			" --confirm " + code + "`. The code works for 15 minutes."}
}

// mailAddr is the API's MailAddress.
type mailAddr struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// sharedNudge says a send goes from mailgoose's shared sending domain
// (MG-85), and how to send from the sender's own domain instead.
func sharedNudge(from, replyTo, sender string) string {
	domain := sender
	if at := strings.LastIndexByte(sender, '@'); at >= 0 {
		domain = sender[at+1:]
	}
	return fmt.Sprintf("Sending from %s, mailgoose's shared domain, with replies to %s.\n"+
		"Add your own domain for better deliverability and trust: mailgoose domain add %s", from, replyTo, domain)
}

// listSendProblems puts every problem in a not_ready error's message, one
// per line with how to fix it, so the person sees them all at once.
func listSendProblems(err error, lead string) error {
	var e *Error
	if !errors.As(err, &e) || e.Code != "not_ready" {
		return err
	}
	raw, _ := e.Details["problems"].([]any)
	if len(raw) == 0 {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d %s to fix:", lead, len(raw), plural(len(raw), "thing", "things"))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		msg, _ := m["message"].(string)
		hint, _ := m["hint"].(string)
		fmt.Fprintf(&b, "\n  - %s", msg)
		if hint != "" {
			fmt.Fprintf(&b, "\n    %s", hint)
		}
	}
	e.Message = b.String()
	e.Hint = ""
	return e
}
