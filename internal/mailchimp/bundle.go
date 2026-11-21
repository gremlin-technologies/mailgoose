// Package mailchimp reads Mailchimp's own exports into one Bundle: audiences
// and their members, templates and past campaign HTML (MG-69). The import job
// (MG-70) and the HTML conversion (MG-71) work from a Bundle, and the OAuth
// fetch (MG-74) will build the same one.
//
// It is written from Mailchimp's public help pages on audience and account
// exports and the Marketing API's Account Exports reference. Those pages name
// the columns and the kinds of file but not exact file names, tag and group
// delimiters or timestamp formats, so the reader matches loosely: a member
// file's status comes from a word in its name, columns are matched ignoring
// case, and timestamps take several common layouts. The fixtures in testdata
// are synthetic, built from those pages; check them against a real export
// when one is available.
package mailchimp

import (
	"strings"
	"time"
)

// Status is a member's Mailchimp status.
type Status string

// Mailchimp's member statuses. Transactional is what Mailchimp's exports call
// non-subscribed contacts.
const (
	Subscribed    Status = "subscribed"
	Unsubscribed  Status = "unsubscribed"
	Cleaned       Status = "cleaned"
	Pending       Status = "pending"
	Transactional Status = "transactional"
	Archived      Status = "archived"
)

// Statuses lists every status, in the order reports show them.
var Statuses = []Status{Subscribed, Unsubscribed, Cleaned, Pending, Transactional, Archived}

// Kinds of input.
const (
	SourceAccountExport  = "account_export"
	SourceAudienceExport = "audience_export"
	SourceAudienceCSV    = "audience_csv"
)

// Bundle is everything read from one export.
type Bundle struct {
	// Source is SourceAccountExport, SourceAudienceExport or
	// SourceAudienceCSV.
	Source    string
	Audiences []*Audience
	Templates []*HTMLFile
	// Campaigns are the past campaigns whose HTML the export holds, in the
	// order of their file names.
	Campaigns []*Campaign
	// Ignored lists files the reader didn't use, sorted by path.
	Ignored []IgnoredFile
}

// Members counts the members of every audience.
func (b *Bundle) Members() int {
	n := 0
	for _, a := range b.Audiences {
		n += len(a.Members)
	}
	return n
}

// Audience is one Mailchimp audience.
type Audience struct {
	// Name is the audience's name: its folder in an account export, or
	// Options.AudienceName (default DefaultAudienceName) when the export
	// doesn't say.
	Name string
	// Files are the member files read for it, by path.
	Files   []string
	Members []*Member
	// DroppedFields are the columns kept only by name (decision 6): every
	// column that isn't email, first or last name, tags, a group, a status,
	// opt-in data or a marketing permission. Sorted, without duplicates.
	DroppedFields []string
	// GroupColumns are the columns read as group categories.
	GroupColumns []string
	// PermissionColumns are the columns read as GDPR marketing permissions.
	PermissionColumns []string
	// Skipped are the first MaxSkippedRows rows that weren't read as
	// members, in the order read. SkippedByReason counts every skipped row,
	// listed or not, by reason.
	Skipped         []SkippedRow
	SkippedByReason map[string]int
}

// SkippedRows counts every skipped row, including those past the ones
// Skipped lists.
func (a *Audience) SkippedRows() int {
	n := 0
	for _, c := range a.SkippedByReason {
		n += c
	}
	return n
}

func (a *Audience) skip(file string, row int, reason string) {
	if a.SkippedByReason == nil {
		a.SkippedByReason = map[string]int{}
	}
	a.SkippedByReason[reason]++
	if len(a.Skipped) < MaxSkippedRows {
		a.Skipped = append(a.Skipped, SkippedRow{File: file, Row: row, Reason: reason})
	}
}

// Member is one row of a member file.
type Member struct {
	Email     string
	FirstName string
	LastName  string
	Status    Status
	Tags      []string
	Groups    []Group
	// The opt-in and confirm data, as written; blank when the export has
	// none. OptinAt and ConfirmAt are those times parsed, nil when blank or
	// in a layout the reader doesn't know.
	OptinTime   string
	OptinIP     string
	ConfirmTime string
	ConfirmIP   string
	OptinAt     *time.Time
	ConfirmAt   *time.Time
	// Permissions are the GDPR marketing permissions the member granted,
	// by label, as the export lists them. PermissionsKnown is true when the
	// export said something about them: a permission column with a value
	// for this member.
	Permissions      []string
	PermissionsKnown bool
	// NamesCut is true when a tags, group or permission cell listed more
	// than MaxListNames names, so some were left out.
	NamesCut bool
	// File and Row say where the member came from; the header is row 1.
	File string
	Row  int
}

// Group is one Mailchimp group (interest) a member is in.
type Group struct {
	Category string
	Name     string
}

// RefusesEmail reports whether the member's GDPR permissions refuse email
// marketing: the export says something about them and none of the granted
// ones is about email. A member with no permission data doesn't refuse.
func (m *Member) RefusesEmail() bool {
	if !m.PermissionsKnown {
		return false
	}
	for _, p := range m.Permissions {
		if strings.Contains(normalize(p), "email") || strings.Contains(normalize(p), "e-mail") {
			return false
		}
	}
	return true
}

// SkippedRow is a row the reader couldn't use.
type SkippedRow struct {
	File   string
	Row    int
	Reason string
}

// Why a row was skipped.
const (
	SkipNoEmail       = "no_email"
	SkipUnknownStatus = "unknown_status"
	SkipBadRow        = "bad_row"
)

// HTMLFile is a template's HTML.
type HTMLFile struct {
	// Name is the file name without its extension.
	Name string
	Path string
	HTML string
}

// Campaign is a past campaign's content.
type Campaign struct {
	// Name is the content file's name without its extension.
	Name string
	Path string
	HTML string
	// Text is the campaign's plain-text part, when the export has one.
	Text string
	// Subject, Title and SentAt come from the export's campaigns CSV when a
	// row there names this campaign; blank or nil otherwise.
	Subject string
	Title   string
	SentAt  *time.Time
}
