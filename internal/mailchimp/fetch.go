package mailchimp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Reading a connected account (MG-74). Fetch reads what the customer picks
// through the Marketing API (v3) and writes it out in the shape of
// Mailchimp's account export, so the upload's reader, preview, confirm and
// import job all work on it unchanged. It's written from the API's public
// reference: lists, list members, merge fields, interest categories and
// interests, segments and their members, campaigns and their content, and
// templates.

// pageSize is how many items one list request asks for; 1,000 is the most
// the API returns.
const pageSize = 1000

// MaxFetchAudiences and MaxFetchSegments are the most one fetch takes.
const (
	MaxFetchAudiences = 20
	MaxFetchSegments  = 20
)

// SegmentTagPrefix starts the tag a saved segment becomes.
const SegmentTagPrefix = "segment: "

// Why a fetch is refused, besides the reader's codes.
const (
	ErrUnknownAudience = "unknown_audience"
	ErrUnknownSegment  = "unknown_segment"
)

// AudienceSummary is one audience of a connected account, as the list of
// audiences shows it. Mailchimp's list stats count subscribed, unsubscribed
// and cleaned members; Total is its approximate count of every contact.
type AudienceSummary struct {
	ID           string
	Name         string
	DoubleOptin  bool
	Subscribed   int
	Unsubscribed int
	Cleaned      int
	Total        int
}

// Audiences lists the connected account's audiences, by name.
func (c *Client) Audiences(ctx context.Context) ([]AudienceSummary, error) {
	var out []AudienceSummary
	for offset := 0; ; offset += pageSize {
		var page struct {
			Lists []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				DoubleOptin bool   `json:"double_optin"`
				Stats       struct {
					MemberCount      int `json:"member_count"`
					UnsubscribeCount int `json:"unsubscribe_count"`
					CleanedCount     int `json:"cleaned_count"`
					TotalContacts    int `json:"total_contacts"`
				} `json:"stats"`
			} `json:"lists"`
			TotalItems int `json:"total_items"`
		}
		q := url.Values{"count": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(offset)},
			"fields": {"lists.id,lists.name,lists.double_optin,lists.stats.member_count,lists.stats.unsubscribe_count," +
				"lists.stats.cleaned_count,lists.stats.total_contacts,total_items"}}
		if err := c.Get(ctx, "/3.0/lists", q, &page); err != nil {
			return nil, err
		}
		for _, l := range page.Lists {
			out = append(out, AudienceSummary{ID: l.ID, Name: l.Name, DoubleOptin: l.DoubleOptin,
				Subscribed: l.Stats.MemberCount, Unsubscribed: l.Stats.UnsubscribeCount, Cleaned: l.Stats.CleanedCount,
				Total: l.Stats.TotalContacts})
		}
		if len(page.Lists) == 0 || offset+pageSize >= page.TotalItems {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// FetchRequest is what to read.
type FetchRequest struct {
	AudienceIDs []string
	// SegmentIDs are saved segments of those audiences; each becomes a tag
	// SegmentTagPrefix+name on its members as they are now.
	SegmentIDs []string
	// Campaigns is how many of the newest sent campaigns to read, 0 to 100.
	Campaigns int
	// MaxMembers is the most members it reads, as for an upload; default
	// DefaultMaxMembers.
	MaxMembers int
}

// Steps of a fetch, for its progress.
const (
	StepAudiences = "audiences"
	StepSegments  = "segments"
	StepMembers   = "members"
	StepCampaigns = "campaigns"
)

// FetchProgress is how far a fetch has got.
type FetchProgress struct {
	Step string
	// Audience is the audience being read, during StepMembers.
	Audience     string
	MembersRead  int
	MembersTotal int
}

// Fetched is a fetch's result: Data is a ZIP in the account export's
// shape, read with Read(Data, FetchedFileName, Options).
type Fetched struct {
	Data []byte
	// GroupColumns are the member files' group columns, one per interest
	// category, for Options.GroupColumns.
	GroupColumns []string
	// Segments are the tags made from saved segments.
	Segments []SegmentTag
	// Templates counts the account's own templates. The API doesn't give a
	// template's HTML, so they aren't in Data.
	Templates int
	Members   int
	// DoubleOptin is each audience's own double opt-in setting, by the name
	// Read gives the audience (its folder in Data), for the preview (MG-76).
	DoubleOptin map[string]bool
}

// FetchedFileName is the name a fetched bundle is stored and read under.
const FetchedFileName = "mailchimp-account.zip"

// SegmentTag is a saved segment read as a tag.
type SegmentTag struct {
	Audience string
	Tag      string
	Members  int
}

// Fetch reads req from the account. progress, if not nil, is called as it
// goes; an error from it stops the fetch. Refusals the customer can act on
// are *Error, with the reader's codes or ErrUnknownAudience and
// ErrUnknownSegment; a revoked token is ErrAccessRevoked.
func (c *Client) Fetch(ctx context.Context, req FetchRequest, progress func(FetchProgress) error) (*Fetched, error) {
	if req.MaxMembers <= 0 {
		req.MaxMembers = DefaultMaxMembers
	}
	f := &fetcher{c: c, req: req, progress: progress, out: &Fetched{}}
	if err := f.run(ctx); err != nil {
		return nil, err
	}
	return f.out, nil
}

type fetcher struct {
	c        *Client
	req      FetchRequest
	progress func(FetchProgress) error
	out      *Fetched
	at       FetchProgress
	zw       *zip.Writer
	buf      bytes.Buffer
	// written counts the bytes put into the ZIP before compression, held to
	// DefaultMaxBytes as the reader holds an upload.
	written int64
}

// fetchAudience is one audience being read.
type fetchAudience struct {
	id, name, folder string
	byStatus         map[Status]int
	merge            []mergeField
	categories       []string
	interests        map[string]Group // by interest id
	tags             map[string][]string
}

type mergeField struct{ tag, column string }

func (f *fetcher) report(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.progress == nil {
		return nil
	}
	return f.progress(f.at)
}

func (f *fetcher) run(ctx context.Context) error {
	f.zw = zip.NewWriter(&f.buf)
	f.at.Step = StepAudiences
	if err := f.report(ctx); err != nil {
		return err
	}
	auds, err := f.audiences(ctx)
	if err != nil {
		return err
	}
	f.at.Step = StepSegments
	if err := f.report(ctx); err != nil {
		return err
	}
	if err := f.segments(ctx, auds); err != nil {
		return err
	}
	f.at.Step = StepMembers
	for _, a := range auds {
		if err := f.members(ctx, a); err != nil {
			return err
		}
	}
	f.at.Step, f.at.Audience = StepCampaigns, ""
	if err := f.report(ctx); err != nil {
		return err
	}
	if err := f.campaigns(ctx); err != nil {
		return err
	}
	if err := f.templates(ctx); err != nil {
		return err
	}
	if err := f.zw.Close(); err != nil {
		return err
	}
	if f.buf.Len() > MaxUploadBytes {
		return refuse(ErrTooLarge, "Those audiences come to more than the %d MB one import takes. Fetch fewer audiences at a time.", MaxUploadBytes>>20)
	}
	f.out.Data = f.buf.Bytes()
	f.out.Members = f.at.MembersRead
	return nil
}

// apiNotFound reports whether err is the API's 404.
func apiNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == 404
}

// audiences reads each audience's name, member counts, merge fields and
// interests, and refuses past MaxMembers before reading any member.
func (f *fetcher) audiences(ctx context.Context) ([]*fetchAudience, error) {
	var auds []*fetchAudience
	folders := map[string]bool{}
	for _, id := range f.req.AudienceIDs {
		var l struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DoubleOptin bool   `json:"double_optin"`
		}
		err := f.c.Get(ctx, "/3.0/lists/"+url.PathEscape(id), url.Values{"fields": {"id,name,double_optin"}}, &l)
		if apiNotFound(err) {
			return nil, refuse(ErrUnknownAudience, "The connected Mailchimp account has no audience %s. `mailgoose import mailchimp audiences` lists them.", id)
		}
		if err != nil {
			return nil, err
		}
		a := &fetchAudience{id: id, name: strings.TrimSpace(l.Name), byStatus: map[Status]int{}, interests: map[string]Group{},
			tags: map[string][]string{}}
		a.folder = folderName(a.name, folders)
		if f.out.DoubleOptin == nil {
			f.out.DoubleOptin = map[string]bool{}
		}
		f.out.DoubleOptin[a.folder] = l.DoubleOptin
		for _, st := range Statuses {
			var page struct {
				TotalItems int `json:"total_items"`
			}
			if err := f.c.Get(ctx, "/3.0/lists/"+url.PathEscape(id)+"/members",
				url.Values{"status": {string(st)}, "count": {"1"}, "fields": {"total_items"}}, &page); err != nil {
				return nil, err
			}
			a.byStatus[st] = page.TotalItems
			f.at.MembersTotal += page.TotalItems
		}
		if f.at.MembersTotal > f.req.MaxMembers {
			return nil, refuse(ErrTooManyMembers, "Those audiences have more than %s contacts, more than one import takes during the beta. Fetch one audience at a time, or contact support to have the limit raised.", commas(f.req.MaxMembers))
		}
		if err := f.mergeFields(ctx, a); err != nil {
			return nil, err
		}
		if err := f.interests(ctx, a); err != nil {
			return nil, err
		}
		auds = append(auds, a)
	}
	return auds, nil
}

// unsafeFolder matches what a folder name in the ZIP can't hold.
var unsafeFolder = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]+`)

// folderName is the ZIP folder an audience is written to: its name, made
// safe, kept clear of the export's other folders and of the audiences
// before it, so the reader names it the same.
func folderName(name string, used map[string]bool) string {
	n := strings.TrimSpace(unsafeFolder.ReplaceAllString(name, "-"))
	n = strings.Trim(n, ". ")
	switch strings.ToLower(n) {
	case "", "audiences", "lists", "audience", "list", "templates", "campaigns_content", "reports", "aggregate_activity",
		"geo_data", "granular_activity", "gallery", "gallery_files", "ecommerce", "sms", "events", "appointments", "__macosx":
		n = strings.TrimSpace(n + " audience")
	}
	base := n
	for i := 2; used[strings.ToLower(n)]; i++ {
		n = fmt.Sprintf("%s (%d)", base, i)
	}
	used[strings.ToLower(n)] = true
	return n
}

// reservedColumns are the member file's own columns; a merge field or an
// interest category with one of these names is written under another so
// the reader doesn't take it for them.
var reservedColumns = map[string]bool{
	"email": true, "email address": true, "e-mail": true, "e-mail address": true, "fname": true, "first name": true,
	"lname": true, "last name": true, "tags": true, "status": true, "email marketing": true, "email marketing status": true,
	"subscription status": true, "member status": true, "optin time": true, "optin ip": true, "confirm time": true,
	"confirm ip": true, "marketing permissions": true,
}

func columnFree(name string, taken map[string]bool) bool {
	n := normalize(name)
	return n != "" && !reservedColumns[n] && !taken[n] && !strings.Contains(n, "marketing permission") && !strings.HasPrefix(n, "gdpr")
}

func (f *fetcher) mergeFields(ctx context.Context, a *fetchAudience) error {
	taken := map[string]bool{}
	for offset := 0; ; offset += pageSize {
		var page struct {
			MergeFields []struct {
				Tag  string `json:"tag"`
				Name string `json:"name"`
			} `json:"merge_fields"`
			TotalItems int `json:"total_items"`
		}
		if err := f.c.Get(ctx, "/3.0/lists/"+url.PathEscape(a.id)+"/merge-fields", url.Values{"count": {strconv.Itoa(pageSize)},
			"offset": {strconv.Itoa(offset)}, "fields": {"merge_fields.tag,merge_fields.name,total_items"}}, &page); err != nil {
			return err
		}
		for _, m := range page.MergeFields {
			if m.Tag == "FNAME" || m.Tag == "LNAME" || m.Tag == "" {
				continue
			}
			col := strings.TrimSpace(m.Name)
			if !columnFree(col, taken) {
				col = strings.TrimSpace(col + " (" + m.Tag + ")")
			}
			if !columnFree(col, taken) {
				continue
			}
			taken[normalize(col)] = true
			a.merge = append(a.merge, mergeField{tag: m.Tag, column: col})
		}
		if len(page.MergeFields) == 0 || offset+pageSize >= page.TotalItems {
			break
		}
	}
	if len(a.merge) > MaxColumns/2 {
		a.merge = a.merge[:MaxColumns/2]
	}
	return nil
}

func (f *fetcher) interests(ctx context.Context, a *fetchAudience) error {
	var cats struct {
		Categories []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"categories"`
	}
	if err := f.c.Get(ctx, "/3.0/lists/"+url.PathEscape(a.id)+"/interest-categories", url.Values{"count": {strconv.Itoa(pageSize)},
		"fields": {"categories.id,categories.title"}}, &cats); err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, m := range a.merge {
		taken[normalize(m.column)] = true
	}
	for _, cat := range cats.Categories {
		title := strings.TrimSpace(cat.Title)
		if !columnFree(title, taken) {
			title = strings.TrimSpace(title + " (group)")
		}
		if !columnFree(title, taken) || len(a.categories) == MaxGroupColumns {
			continue
		}
		taken[normalize(title)] = true
		a.categories = append(a.categories, title)
		var page struct {
			Interests []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"interests"`
		}
		if err := f.c.Get(ctx, "/3.0/lists/"+url.PathEscape(a.id)+"/interest-categories/"+url.PathEscape(cat.ID)+"/interests",
			url.Values{"count": {strconv.Itoa(pageSize)}, "fields": {"interests.id,interests.name"}}, &page); err != nil {
			return err
		}
		for _, in := range page.Interests {
			a.interests[in.ID] = Group{Category: title, Name: strings.TrimSpace(in.Name)}
		}
	}
	for _, c := range a.categories {
		f.out.GroupColumns = insertSorted(f.out.GroupColumns, c)
	}
	return nil
}

// segments finds each saved segment in the audiences and notes its members
// for their tag.
func (f *fetcher) segments(ctx context.Context, auds []*fetchAudience) error {
	for _, sid := range f.req.SegmentIDs {
		var found *fetchAudience
		var seg struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		for _, a := range auds {
			err := f.c.Get(ctx, "/3.0/lists/"+url.PathEscape(a.id)+"/segments/"+url.PathEscape(sid), url.Values{"fields": {"id,name"}}, &seg)
			if apiNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			found = a
			break
		}
		if found == nil {
			return refuse(ErrUnknownSegment, "None of those audiences has a saved segment %s. Pick a segment of an audience you're fetching.", sid)
		}
		tag := SegmentTagPrefix + strings.TrimSpace(seg.Name)
		n := 0
		for offset := 0; ; offset += pageSize {
			var page struct {
				Members []struct {
					Email string `json:"email_address"`
				} `json:"members"`
				TotalItems int `json:"total_items"`
			}
			if err := f.c.Get(ctx, "/3.0/lists/"+url.PathEscape(found.id)+"/segments/"+url.PathEscape(sid)+"/members",
				url.Values{"count": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(offset)},
					"fields": {"members.email_address,total_items"}}, &page); err != nil {
				return err
			}
			for _, m := range page.Members {
				e := strings.ToLower(strings.TrimSpace(m.Email))
				if e == "" {
					continue
				}
				found.tags[e] = append(found.tags[e], tag)
				n++
			}
			if len(page.Members) == 0 || offset+pageSize >= page.TotalItems || n > f.req.MaxMembers {
				break
			}
		}
		f.out.Segments = append(f.out.Segments, SegmentTag{Audience: found.folder, Tag: tag, Members: n})
	}
	return nil
}

// apiMember is a list member as the API gives it.
type apiMember struct {
	Email       string          `json:"email_address"`
	Status      string          `json:"status"`
	MergeFields map[string]any  `json:"merge_fields"`
	Interests   map[string]bool `json:"interests"`
	Tags        []struct {
		Name string `json:"name"`
	} `json:"tags"`
	TimestampSignup      string `json:"timestamp_signup"`
	IPSignup             string `json:"ip_signup"`
	TimestampOpt         string `json:"timestamp_opt"`
	IPOpt                string `json:"ip_opt"`
	MarketingPermissions []struct {
		Text    string `json:"text"`
		Enabled bool   `json:"enabled"`
	} `json:"marketing_permissions"`
}

const memberFields = "members.email_address,members.status,members.merge_fields,members.interests,members.tags," +
	"members.timestamp_signup,members.ip_signup,members.timestamp_opt,members.ip_opt,members.marketing_permissions,total_items"

// noPermissions is the permissions cell of a member who has GDPR
// permissions and granted none, so the reader knows they refuse email.
const noPermissions = "None granted"

// members writes one file per status of the audience, paging through its
// members.
func (f *fetcher) members(ctx context.Context, a *fetchAudience) error {
	f.at.Audience = a.name
	if err := f.report(ctx); err != nil {
		return err
	}
	header := []string{"Email Address", "First Name", "Last Name"}
	for _, m := range a.merge {
		header = append(header, m.column)
	}
	header = append(header, a.categories...)
	header = append(header, "TAGS", "OPTIN_TIME", "OPTIN_IP", "CONFIRM_TIME", "CONFIRM_IP", "Marketing Permissions")
	for _, st := range Statuses {
		// Every audience gets its subscribed file, so an empty one still
		// shows in the preview.
		if a.byStatus[st] == 0 && st != Subscribed {
			continue
		}
		w, err := f.create("audiences/" + a.folder + "/" + string(st) + "_members.csv")
		if err != nil {
			return err
		}
		cw := csv.NewWriter(w)
		if err := cw.Write(header); err != nil {
			return err
		}
		for offset := 0; ; offset += pageSize {
			var page struct {
				Members    []apiMember `json:"members"`
				TotalItems int         `json:"total_items"`
			}
			if err := f.c.Get(ctx, "/3.0/lists/"+url.PathEscape(a.id)+"/members", url.Values{"status": {string(st)},
				"count": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(offset)}, "fields": {memberFields}}, &page); err != nil {
				return err
			}
			for i := range page.Members {
				if err := cw.Write(a.row(&page.Members[i], len(header))); err != nil {
					return err
				}
			}
			f.at.MembersRead += len(page.Members)
			if f.at.MembersRead > f.req.MaxMembers {
				return refuse(ErrTooManyMembers, "Those audiences have more than %s contacts, more than one import takes during the beta. Fetch one audience at a time, or contact support to have the limit raised.", commas(f.req.MaxMembers))
			}
			cw.Flush()
			if err := cw.Error(); err != nil {
				return err
			}
			if err := f.report(ctx); err != nil {
				return err
			}
			if len(page.Members) < pageSize {
				break
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return err
		}
	}
	return nil
}

// row is a member's line in the member file.
func (a *fetchAudience) row(m *apiMember, n int) []string {
	rec := make([]string, 0, n)
	email := strings.TrimSpace(m.Email)
	rec = append(rec, email, mergeText(m.MergeFields["FNAME"]), mergeText(m.MergeFields["LNAME"]))
	for _, mf := range a.merge {
		rec = append(rec, mergeText(m.MergeFields[mf.tag]))
	}
	byCat := map[string][]string{}
	for id, in := range m.Interests {
		if g, ok := a.interests[id]; in && ok {
			byCat[g.Category] = append(byCat[g.Category], g.Name)
		}
	}
	for _, c := range a.categories {
		sort.Strings(byCat[c])
		rec = append(rec, listCell(byCat[c]))
	}
	tags := make([]string, 0, len(m.Tags))
	for _, t := range m.Tags {
		tags = append(tags, t.Name)
	}
	tags = append(tags, a.tags[strings.ToLower(email)]...)
	rec = append(rec, listCell(tags), m.TimestampSignup, m.IPSignup, m.TimestampOpt, m.IPOpt)
	perm := ""
	if len(m.MarketingPermissions) > 0 {
		var granted []string
		for _, p := range m.MarketingPermissions {
			if p.Enabled {
				granted = append(granted, p.Text)
			}
		}
		perm = listCell(granted)
		if perm == "" {
			perm = noPermissions
		}
	}
	return append(rec, perm)
}

// mergeText is a merge field's value as text: an address's parts joined,
// anything else as written.
func mergeText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case map[string]any:
		var parts []string
		for _, k := range []string{"addr1", "addr2", "city", "state", "zip", "country"} {
			if s, ok := x[k].(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

// listCell is names as the reader splits a cell: by commas, a name with a
// comma in double quotes. A name's own double quotes become single ones.
func listCell(names []string) string {
	var b strings.Builder
	n := 0
	for _, s := range names {
		s = strings.TrimSpace(strings.ReplaceAll(s, `"`, "'"))
		if s == "" {
			continue
		}
		if n > 0 {
			b.WriteByte(',')
		}
		n++
		if strings.ContainsAny(s, ",\r\n") {
			b.WriteString(`"` + s + `"`)
		} else {
			b.WriteString(s)
		}
	}
	return b.String()
}

// campaigns writes the newest sent campaigns' content and a campaigns CSV
// with their subjects and send times.
func (f *fetcher) campaigns(ctx context.Context) error {
	if f.req.Campaigns <= 0 {
		return nil
	}
	var page struct {
		Campaigns []struct {
			ID       string `json:"id"`
			SendTime string `json:"send_time"`
			Settings struct {
				Subject string `json:"subject_line"`
				Title   string `json:"title"`
			} `json:"settings"`
		} `json:"campaigns"`
	}
	if err := f.c.Get(ctx, "/3.0/campaigns", url.Values{"status": {"sent"}, "sort_field": {"send_time"}, "sort_dir": {"DESC"},
		"count":  {strconv.Itoa(f.req.Campaigns)},
		"fields": {"campaigns.id,campaigns.send_time,campaigns.settings.subject_line,campaigns.settings.title"}}, &page); err != nil {
		return err
	}
	var rows [][]string
	for _, c := range page.Campaigns {
		name := safeFileName(c.ID)
		if name == "" {
			continue
		}
		var content struct {
			HTML      string `json:"html"`
			PlainText string `json:"plain_text"`
		}
		err := f.c.Get(ctx, "/3.0/campaigns/"+url.PathEscape(c.ID)+"/content", url.Values{"fields": {"html,plain_text"}}, &content)
		if apiNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(content.HTML) == "" {
			continue
		}
		if err := f.file("campaigns_content/"+name+".html", content.HTML); err != nil {
			return err
		}
		if strings.TrimSpace(content.PlainText) != "" {
			if err := f.file("campaigns_content/"+name+".txt", content.PlainText); err != nil {
				return err
			}
		}
		rows = append(rows, []string{name, c.Settings.Title, c.Settings.Subject, c.SendTime})
	}
	if len(rows) == 0 {
		return nil
	}
	w, err := f.create("campaigns.csv")
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"Unique Id", "Title", "Subject", "Send Date"})
	_ = cw.WriteAll(rows)
	return cw.Error()
}

var unsafeFileName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func safeFileName(s string) string {
	s = unsafeFileName.ReplaceAllString(s, "")
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// templates counts the account's own templates. Their HTML isn't in the
// Marketing API, so the account export brings them.
func (f *fetcher) templates(ctx context.Context) error {
	var page struct {
		TotalItems int `json:"total_items"`
	}
	if err := f.c.Get(ctx, "/3.0/templates", url.Values{"type": {"user"}, "count": {"1"}, "fields": {"total_items"}}, &page); err != nil {
		return err
	}
	f.out.Templates = page.TotalItems
	return nil
}

// create starts a file in the ZIP; what's written to it counts toward
// DefaultMaxBytes.
func (f *fetcher) create(name string) (io.Writer, error) {
	w, err := f.zw.Create(name)
	if err != nil {
		return nil, err
	}
	return &countingWriter{w: w, f: f}, nil
}

func (f *fetcher) file(name, body string) error {
	w, err := f.create(name)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, body)
	return err
}

type countingWriter struct {
	w io.Writer
	f *fetcher
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.f.written += int64(len(p))
	if c.f.buf.Len() > MaxUploadBytes {
		return 0, refuse(ErrTooLarge, "Those audiences come to more than the %d MB one import takes. Fetch fewer audiences at a time.", MaxUploadBytes>>20)
	}
	if c.f.written > DefaultMaxBytes {
		return 0, refuse(ErrTooLarge, "Those audiences come to more than %s once read, more than one import takes. Fetch fewer audiences at a time.", humanBytes(DefaultMaxBytes))
	}
	return c.w.Write(p)
}
