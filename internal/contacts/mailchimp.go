package contacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// Mailchimp import (MG-70). A bundle read from a Mailchimp export (MG-69) is
// merged into one row per address, previewed without writing anything, and
// on confirm stored as an import of kind "mailchimp" that the import worker
// runs like a CSV import: in batches, with the same holds and skip report.
//
// The reversible defaults (owner decisions 1, 2, 3, 4, 6 and 7, and the
// details in docs/decisions.md):
//   - subscribed stays subscribed with no confirmation email, pending becomes
//     unconfirmed with no email sent, unsubscribed stays unsubscribed and
//     cleaned becomes bounced; non-subscribed and archived are skipped. A
//     member whose GDPR permissions refuse email marketing comes in
//     unsubscribed.
//   - Audiences merge into the one mailgoose audience, and an address
//     unsubscribed or cleaned in any of them is suppressed.
//   - An existing contact only gains tags; one that is subscribed or
//     unconfirmed is unsubscribed or bounced when Mailchimp says so, and one
//     that unsubscribed, bounced or complained is never subscribed.
//   - Unsubscribed and bounced contacts always import. When the subscribed
//     part would take a capped account past its limit, the import waits
//     with its suppressions in and nothing else, until the plan covers it.

// Kinds of import.
const (
	ImportKindCSV       = "csv"
	ImportKindMailchimp = "mailchimp"
)

// ImportWaiting is a Mailchimp import whose subscribed part waits for a plan
// that covers it, and ImportCancelled one the customer cancelled while it
// waited.
const (
	ImportWaiting   = "waiting"
	ImportCancelled = "cancelled"
)

// Why a Mailchimp row was skipped, beyond the CSV import's reasons.
const (
	// SkipNotSubscribed: non-subscribed (transactional) in Mailchimp, never
	// opted in to marketing.
	SkipNotSubscribed = "not_subscribed"
	// SkipArchived: archived in Mailchimp.
	SkipArchived = "archived"
)

// Limits on a Mailchimp import.
const (
	// MaxMailchimpTags is the most different tags one Mailchimp import may
	// put on its contacts, counting groups and audience tags.
	MaxMailchimpTags = 1_000
	// mailchimpAudiencesKept is how many of an address's audiences its
	// consent record lists.
	mailchimpAudiencesKept = 20
	// mergeExamples is how many addresses the preview lists as suppressed by
	// merging audiences.
	mergeExamples = 100
	// mailchimpListDetail is the list source detail of every Mailchimp
	// import: list source "export", "another email tool".
	mailchimpListDetail = "Mailchimp"
)

// MailchimpSummary is what a bundle held, kept with the import and shown in
// its preview.
type MailchimpSummary struct {
	// Source is the kind of export: mailchimp.SourceAccountExport,
	// SourceAudienceExport or SourceAudienceCSV.
	Source    string              `json:"source"`
	Audiences []MailchimpAudience `json:"audiences"`
	// ByMailchimpStatus counts member rows by their Mailchimp status, every
	// status present.
	ByMailchimpStatus map[string]int `json:"by_mailchimp_status"`
	// DroppedFields are the columns not imported (decision 6), across
	// audiences.
	DroppedFields []string `json:"dropped_fields"`
	// RenamedTags are tags whose Mailchimp names mailgoose can't take as
	// they are: cut to MaxTagNameLen characters, or with commas or line
	// breaks taken out.
	RenamedTags []TagRename `json:"renamed_tags"`
	// MergeSuppressed counts addresses subscribed or pending in one audience
	// but unsubscribed or cleaned in another, so suppressed (decision 3).
	MergeSuppressed int `json:"merge_suppressed"`
	// Subscribed counts the addresses coming in subscribed, WithOptinTime
	// those of them with a Mailchimp opt-in time, and NewestOptin the latest
	// such time: the signals for a held import (MG-77).
	Subscribed    int        `json:"subscribed"`
	WithOptinTime int        `json:"with_optin_time"`
	NewestOptin   *time.Time `json:"newest_optin,omitempty"`
	// NamesCut counts members with a tags, group or permission cell longer
	// than the reader takes.
	NamesCut  int `json:"names_cut"`
	Templates int `json:"templates"`
	Campaigns int `json:"campaigns"`
}

// MailchimpAudience is one audience of a bundle.
type MailchimpAudience struct {
	Name string `json:"name"`
	// Members counts its member rows, ByStatus by Mailchimp status.
	Members  int            `json:"members"`
	ByStatus map[string]int `json:"by_status"`
}

// TagRename is a Mailchimp tag name and the name it's imported under.
type TagRename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// mcRow is one line of a Mailchimp import's stored data: one address and
// what to do with it, or one skipped row.
type mcRow struct {
	// Ref is where the row came from, "file:row"; for an address, its first
	// member row.
	Ref string `json:"ref"`
	// Email is the address, normalized; for a skipped row, the cell as
	// written.
	Email  string   `json:"email"`
	First  string   `json:"first,omitempty"`
	Last   string   `json:"last,omitempty"`
	Status string   `json:"status,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	// Consent is the contact's consent record wording, without the file
	// name, which the worker adds.
	Consent json.RawMessage `json:"consent,omitempty"`
	// Skip is why the row is skipped; empty for an address to import.
	Skip string `json:"skip,omitempty"`
}

// mcPlan is a bundle merged into rows: skips first, then the addresses that
// come in suppressed, then the counted ones from countedFrom on.
type mcPlan struct {
	rows        []mcRow
	countedFrom int
	summary     MailchimpSummary
	// tags are every tag name the rows use, distinct ignoring case.
	tags []string
	// merged are the addresses MergeSuppressed counts, in row order.
	merged []string
	// unlisted counts the reader's skipped rows past those it lists.
	unlisted int
	// invalid counts rows that aren't an address: the list checks' invalid
	// rows.
	invalid int
}

// mcRank orders Mailchimp statuses for merging an address's members: the
// highest wins. Skipped statuses rank 0.
func mcRank(m *mailchimp.Member) (rank int, status string) {
	if m.RefusesEmail() && m.Status != mailchimp.Cleaned {
		return 3, StatusUnsubscribed
	}
	switch m.Status {
	case mailchimp.Cleaned:
		return 4, StatusBounced
	case mailchimp.Unsubscribed:
		return 3, StatusUnsubscribed
	case mailchimp.Subscribed:
		return 2, StatusSubscribed
	case mailchimp.Pending:
		return 1, StatusUnconfirmed
	}
	return 0, ""
}

// MailchimpGroupTag is the tag a member's Mailchimp group becomes, or ""
// when nothing is left of its name.
func MailchimpGroupTag(g mailchimp.Group) string { return mcTagName(g.Category + ": " + g.Name) }

// MailchimpAudienceTag is the tag that says which audience a contact came
// from, put on them when an import has more than one.
func MailchimpAudienceTag(name string) string { return mcTagName("audience: " + name) }

// mcTagName is a Mailchimp tag or group name as a mailgoose tag name: commas
// and line breaks become spaces and it's cut to MaxTagNameLen characters.
// It returns "" when nothing is left.
func mcTagName(raw string) string {
	n := strings.Map(func(r rune) rune {
		if r == ',' || r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, raw)
	n = strings.Join(strings.Fields(n), " ")
	if utf8.RuneCountInString(n) > MaxTagNameLen {
		n = strings.TrimSpace(string([]rune(n)[:MaxTagNameLen]))
	}
	return n
}

func mcRef(m *mailchimp.Member) string { return fmt.Sprintf("%s:%d", m.File, m.Row) }

// readerSkip is the import skip reason for a row the reader couldn't use.
func readerSkip(reason string) string {
	if reason == mailchimp.SkipNoEmail {
		return SkipInvalidEmail
	}
	return SkipInvalidRow
}

type mcConsentAudience struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	OptinTime   string   `json:"optin_time,omitempty"`
	OptinIP     string   `json:"optin_ip,omitempty"`
	ConfirmTime string   `json:"confirm_time,omitempty"`
	ConfirmIP   string   `json:"confirm_ip,omitempty"`
	Permissions []string `json:"gdpr_permissions,omitempty"`
}

// planMailchimp merges a bundle's members into one row per address.
func planMailchimp(b *mailchimp.Bundle) (*mcPlan, error) {
	p := &mcPlan{}
	sum := &p.summary
	sum.Source = b.Source
	sum.Templates, sum.Campaigns = len(b.Templates), len(b.Campaigns)
	sum.ByMailchimpStatus = map[string]int{}
	for _, st := range mailchimp.Statuses {
		sum.ByMailchimpStatus[string(st)] = 0
	}
	sum.DroppedFields, sum.RenamedTags = []string{}, []TagRename{}

	type address struct {
		members []*mailchimp.Member
		// memberAud is each member's audience; audiences the distinct ones.
		memberAud, audiences []string
	}
	n := b.Members()
	byEmail := make(map[string]*address, n)
	order := make([]string, 0, n)
	var skips []mcRow
	renamed := map[string]bool{}
	multi := len(b.Audiences) > 1
	for _, a := range b.Audiences {
		ma := MailchimpAudience{Name: a.Name, Members: len(a.Members), ByStatus: map[string]int{}}
		for _, st := range mailchimp.Statuses {
			ma.ByStatus[string(st)] = 0
		}
		for _, f := range a.DroppedFields {
			if i, found := slices.BinarySearch(sum.DroppedFields, f); !found {
				sum.DroppedFields = slices.Insert(sum.DroppedFields, i, f)
			}
		}
		for _, sk := range a.Skipped {
			skips = append(skips, mcRow{Ref: fmt.Sprintf("%s:%d", sk.File, sk.Row), Skip: readerSkip(sk.Reason)})
		}
		p.unlisted += a.SkippedRows() - len(a.Skipped)
		p.invalid += a.SkippedRows()
		for _, m := range a.Members {
			ma.ByStatus[string(m.Status)]++
			sum.ByMailchimpStatus[string(m.Status)]++
			if m.NamesCut {
				sum.NamesCut++
			}
			e, err := NormalizeEmail(m.Email)
			if err != nil {
				skips = append(skips, mcRow{Ref: mcRef(m), Email: clip(m.Email, 254), Skip: SkipInvalidEmail})
				p.invalid++
				continue
			}
			ad := byEmail[e]
			if ad == nil {
				ad = &address{}
				byEmail[e] = ad
				order = append(order, e)
			}
			ad.members = append(ad.members, m)
			ad.memberAud = append(ad.memberAud, a.Name)
			if !slices.Contains(ad.audiences, a.Name) {
				ad.audiences = append(ad.audiences, a.Name)
			}
		}
		sum.Audiences = append(sum.Audiences, ma)
	}

	tagSeen := map[string]bool{}
	// Sized up front: growing a slice of rows this size holds two copies.
	var suppressing []mcRow
	counted := make([]mcRow, 0, len(order))
	for _, e := range order {
		ad := byEmail[e]
		best, status := -1, ""
		var decider *mailchimp.Member
		wanted := false // subscribed or pending somewhere
		for _, m := range ad.members {
			r, st := mcRank(m)
			if r == 1 || r == 2 {
				wanted = true
			}
			if r > best {
				best, status, decider = r, st, m
			}
		}
		row := mcRow{Ref: mcRef(ad.members[0]), Email: e}
		if best == 0 {
			row.Email = ad.members[0].Email
			row.Skip = SkipArchived
			for _, m := range ad.members {
				if m.Status == mailchimp.Transactional {
					row.Skip = SkipNotSubscribed
				}
			}
			skips = append(skips, row)
			continue
		}
		row.Status = status
		for _, m := range ad.members {
			if row.First == "" {
				row.First = m.FirstName
			}
			if row.Last == "" {
				row.Last = m.LastName
			}
		}

		var tags []string
		mine := map[string]bool{}
		addTag := func(raw string) {
			n := mcTagName(raw)
			if n == "" {
				return
			}
			if n != strings.Join(strings.Fields(raw), " ") && !renamed[raw] && len(sum.RenamedTags) < MaxMailchimpTags {
				renamed[raw] = true
				sum.RenamedTags = append(sum.RenamedTags, TagRename{From: raw, To: n})
			}
			k := strings.ToLower(n)
			if mine[k] {
				return
			}
			mine[k] = true
			tags = append(tags, n)
			if !tagSeen[k] {
				tagSeen[k] = true
				p.tags = append(p.tags, n)
			}
		}
		var consent []mcConsentAudience
		for i, m := range ad.members {
			for _, t := range m.Tags {
				addTag(t)
			}
			for _, g := range m.Groups {
				addTag(g.Category + ": " + g.Name)
			}
			if i < mailchimpAudiencesKept {
				consent = append(consent, mcConsentAudience{Name: ad.memberAud[i], Status: string(m.Status),
					OptinTime: m.OptinTime, OptinIP: m.OptinIP, ConfirmTime: m.ConfirmTime, ConfirmIP: m.ConfirmIP,
					Permissions: m.Permissions})
			}
		}
		if multi {
			for _, a := range ad.audiences {
				addTag("audience: " + a)
			}
		}
		if len(p.tags) > MaxMailchimpTags {
			return nil, invalidImport("too_many_tags", "bundle", fmt.Sprintf(
				"The export puts more than %s different tags and groups on its contacts. Remove the ones you don't need in Mailchimp, or import one audience at a time.",
				commaNumber(MaxMailchimpTags)))
		}
		row.Tags = tags
		wording, err := json.Marshal(map[string]any{
			"list_source": ListSourceExport, "list_source_detail": mailchimpListDetail,
			"mailchimp": map[string]any{"audiences": consent},
		})
		if err != nil {
			return nil, err
		}
		row.Consent = wording

		if Counted(status) {
			if status == StatusSubscribed {
				sum.Subscribed++
				if decider.OptinTime != "" {
					sum.WithOptinTime++
				}
				if t := decider.OptinAt; t != nil && (sum.NewestOptin == nil || t.After(*sum.NewestOptin)) {
					sum.NewestOptin = t
				}
			}
			counted = append(counted, row)
		} else {
			if wanted {
				sum.MergeSuppressed++
				p.merged = append(p.merged, e)
			}
			suppressing = append(suppressing, row)
		}
	}
	p.rows = make([]mcRow, 0, len(skips)+len(suppressing)+len(counted))
	p.rows = append(append(append(p.rows, skips...), suppressing...), counted...)
	p.countedFrom = len(skips) + len(suppressing)
	return p, nil
}

// data is the plan's rows as stored: JSON lines.
func (p *mcPlan) data() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for i := range p.rows {
		if err := enc.Encode(&p.rows[i]); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// contentHash identifies a bundle's rows, for confirming it twice.
func contentHash(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// decodeRows reads a Mailchimp import's stored rows.
func decodeRows(data []byte) ([]mcRow, error) {
	var rows []mcRow
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var r mcRow
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("read stored Mailchimp row %d: %w", len(rows)+1, err)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// commaNumber writes n with thousands separators.
func commaNumber(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
