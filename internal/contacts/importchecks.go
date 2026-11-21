package contacts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/emaillists"
)

// Import list checks and list source (MG-63).
//
// An import carries no proof that its people asked for mail, the way a form
// signup does, so every import says where its list came from, and its
// addresses are checked when it's created: bad syntax, role addresses
// (info@, sales@ ...), disposable domains, and how many of them other
// accounts imported too, compared as hashes. Bought, scraped and shared
// lists show up in those numbers; such an import is held for review with
// the existing hold (MG-34).

// List sources: where the customer says an import's people came from.
const (
	ListSourceForm      = "form"      // signed up on my site or form
	ListSourceCustomers = "customers" // customers who bought from me
	ListSourcePersonal  = "personal"  // people I know personally
	ListSourceExport    = "export"    // another email tool (an export)
	ListSourceOther     = "other"     // other, described in list_source_detail
)

// ListSources lists every list source, in the order forms show them.
var ListSources = []string{ListSourceForm, ListSourceCustomers, ListSourcePersonal, ListSourceExport, ListSourceOther}

// MaxListSourceDetail is the longest description of an "other" list source.
const MaxListSourceDetail = 200

// More reasons an import is held (MG-63), checked in this order; the first
// that applies is the one recorded.
const (
	// HoldSharedList: many of its addresses are in other accounts' imports.
	HoldSharedList = "shared_list"
	// HoldRiskyAddresses: too many role and disposable addresses.
	HoldRiskyAddresses = "risky_addresses"
	// HoldInvalidAddresses: too many rows that aren't email addresses.
	HoldInvalidAddresses = "invalid_addresses"
)

// ImportChecks are the thresholds that hold an import for review. They're
// first guesses, to tune once real imports come in.
type ImportChecks struct {
	// RiskyPercent: role plus disposable addresses over this percentage of
	// the valid rows.
	RiskyPercent int
	// InvalidPercent: rows that aren't addresses over this percentage of
	// all rows.
	InvalidPercent int
	// SharedPercent and SharedMin: at least this percentage of the file's
	// distinct addresses, and at least SharedMin of them, are already in
	// other accounts' imports.
	SharedPercent int
	SharedMin     int
}

// DefaultImportChecks are the thresholds unless config says otherwise.
var DefaultImportChecks = ImportChecks{RiskyPercent: 10, InvalidPercent: 5, SharedPercent: 50, SharedMin: 200}

// orDefault fills thresholds left at zero with the defaults.
func (c ImportChecks) orDefault() ImportChecks {
	d := DefaultImportChecks
	if c.RiskyPercent > 0 {
		d.RiskyPercent = c.RiskyPercent
	}
	if c.InvalidPercent > 0 {
		d.InvalidPercent = c.InvalidPercent
	}
	if c.SharedPercent > 0 {
		d.SharedPercent = c.SharedPercent
	}
	if c.SharedMin > 0 {
		d.SharedMin = c.SharedMin
	}
	return d
}

// CheckCounts are what an import's checks found.
type CheckCounts struct {
	// InvalidRows are rows that aren't an email address, or couldn't be
	// read.
	InvalidRows int
	// RoleAddresses and DisposableAddresses count valid rows, repeats
	// included.
	RoleAddresses       int
	DisposableAddresses int
	// SharedAddresses counts the file's distinct addresses that other
	// accounts imported; SharedAccountID is the account with most of them.
	// Only admins see these.
	SharedAddresses int
	SharedAccountID *string
}

// checkListSource validates an import's list source and returns the
// detail to store.
func checkListSource(source, detail string) (*string, error) {
	detail = strings.Join(strings.Fields(detail), " ")
	switch source {
	case "":
		return nil, invalidImport("list_source_required", "list_source",
			"Say where these contacts came from: form, customers, personal, export or other.")
	case ListSourceOther:
		if detail == "" {
			return nil, invalidImport("list_source_detail_required", "list_source_detail",
				"With list source other, describe in a few words where these contacts came from.")
		}
	case ListSourceForm, ListSourceCustomers, ListSourcePersonal, ListSourceExport:
	default:
		return nil, invalidImport("invalid_list_source", "list_source",
			"list_source must be form, customers, personal, export or other.")
	}
	if utf8.RuneCountInString(detail) > MaxListSourceDetail {
		return nil, invalidImport("invalid_list_source", "list_source_detail",
			fmt.Sprintf("Keep the description to %d characters.", MaxListSourceDetail))
	}
	if detail == "" {
		return nil, nil
	}
	return &detail, nil
}

// fileScan is what reading a file for the checks found.
type fileScan struct {
	total, valid int
	counts       CheckCounts
	// hashes are the file's distinct addresses as importHash.
	hashes [][]byte
}

// add counts one row of the file.
func (f *fileScan) add(r csvRow, seen map[string]struct{}) {
	f.addAddress(r.email, r.unreadable, seen)
}

// addAddress counts one row with this email cell; unreadable is a row that
// couldn't be read at all.
func (f *fileScan) addAddress(email string, unreadable bool, seen map[string]struct{}) {
	f.total++
	e, err := NormalizeEmail(email)
	if unreadable || err != nil {
		f.counts.InvalidRows++
		return
	}
	f.valid++
	if local, domain, ok := emaillists.Address(e); ok {
		if emaillists.Role(local) {
			f.counts.RoleAddresses++
		}
		if emaillists.Disposable(domain) {
			f.counts.DisposableAddresses++
		}
	}
	if _, dup := seen[e]; !dup {
		seen[e] = struct{}{}
		f.hashes = append(f.hashes, importHash(e))
	}
}

// importHash is what's kept of an imported address to compare across
// accounts: the same for every account, unlike the suppression list's.
func importHash(email string) []byte {
	sum := sha256.Sum256([]byte("mailgoose-import\x00" + email))
	return sum[:]
}

// sharedWith counts the hashes other accounts imported, in any of their
// imports, and finds the account with most of them. It reads only counts
// and an account id, never another account's contacts.
func sharedWith(ctx context.Context, tx querier, accountID string, hashes [][]byte) (int, *string, error) {
	if len(hashes) == 0 {
		return 0, nil, nil
	}
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM unnest($2::bytea[]) AS x(hash)
		WHERE EXISTS (SELECT 1 FROM import_address_hashes h WHERE h.hash = x.hash AND h.account_id <> $1)`,
		accountID, hashes).Scan(&n); err != nil {
		return 0, nil, fmt.Errorf("compare addresses: %w", err)
	}
	if n == 0 {
		return 0, nil, nil
	}
	var top string
	err := tx.QueryRow(ctx, `
		SELECT h.account_id::text FROM import_address_hashes h
		WHERE h.hash = ANY($2::bytea[]) AND h.account_id <> $1
		GROUP BY h.account_id ORDER BY count(DISTINCT h.hash) DESC, h.account_id LIMIT 1`,
		accountID, hashes).Scan(&top)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("compare addresses: %w", err)
	}
	return n, &top, nil
}

// holdFor is the reason the checks hold an import for, or "" when they
// don't.
func (c ImportChecks) holdFor(f *fileScan) string {
	c = c.orDefault()
	distinct := len(f.hashes)
	switch {
	case f.counts.SharedAddresses >= c.SharedMin && f.counts.SharedAddresses*100 >= distinct*c.SharedPercent:
		return HoldSharedList
	case f.valid > 0 && (f.counts.RoleAddresses+f.counts.DisposableAddresses)*100 > f.valid*c.RiskyPercent:
		return HoldRiskyAddresses
	case f.total > 0 && f.counts.InvalidRows*100 > f.total*c.InvalidPercent:
		return HoldInvalidAddresses
	}
	return ""
}

// HoldMessage says, in plain words, why an import is held, for the
// customer. It names no other account and no count from one.
func HoldMessage(reason string) string {
	switch reason {
	case HoldLargeFirstImport:
		return "It's your first import and it's a large one, so mailgoose checks it before these contacts get campaigns."
	case HoldOverContactLimit:
		return "It would take the account past its contact limit, so mailgoose checks it before these contacts get campaigns."
	case HoldSharedList:
		return "Many of these addresses are already on lists imported to other mailgoose accounts, which usually means " +
			"a bought, rented or shared list. mailgoose checks it before these contacts get campaigns."
	case HoldRiskyAddresses:
		return "Many of these addresses are role addresses (like info@ or sales@) or throwaway inboxes, which people " +
			"rarely sign up with. mailgoose checks it before these contacts get campaigns."
	case HoldInvalidAddresses:
		return "Many rows in the file aren't valid email addresses, which often means an old or scraped list. " +
			"mailgoose checks it before these contacts get campaigns."
	}
	return ""
}
