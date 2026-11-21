package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/billing"
	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
	"github.com/gremlin-technologies/mailgoose/internal/tester"
)

// MailchimpPreview is what confirming a bundle would do. Making it writes
// nothing.
type MailchimpPreview struct {
	Summary MailchimpSummary
	// New counts the addresses that would become new contacts, by the
	// status they'd have; every status the import gives is present.
	New map[string]int
	// ExistingTagged counts addresses that are already contacts and would
	// only gain tags; ExistingSuppressed those of them, subscribed or
	// unconfirmed now, that Mailchimp's unsubscribe or clean would take off
	// the mailing list.
	ExistingTagged     int
	ExistingSuppressed int
	// Skipped counts rows that wouldn't be imported, by reason: every
	// reason a Mailchimp import skips for is present.
	Skipped map[string]int
	// TagsToCreate are the tags the account doesn't have yet, by name.
	TagsToCreate []string
	// MergeSuppressedExamples are up to 100 of the addresses
	// Summary.MergeSuppressed counts.
	MergeSuppressedExamples []string
	Plan                    PlanNeed
	// Holds are why the import would be held for review before its
	// contacts get campaigns, in the order they're checked; the first is
	// the one recorded. over_contact_limit is there when Waits is.
	Holds []string
	// Waits is true when the subscribed part would take the account past
	// its contact limit: it waits until the plan covers it, with the
	// suppressions already imported.
	Waits bool
}

// PlanNeed is the plan an import needs.
type PlanNeed struct {
	// Plan is the plan that would cover the account after the import,
	// "free" or "paid", and Upgrade is true when that means leaving the
	// free plan.
	Plan    string
	Upgrade bool
	// Contacts is how many counted contacts the account would have.
	Contacts int
	// Limit is the account's contact limit now, 0 when it has none.
	Limit int
	// Tester is true for a tester account, whose limit no plan lifts.
	Tester bool
	// Units, MonthCents and YearCents are what the paid plan would cost
	// for Contacts; zero when the free plan covers them.
	Units      int
	MonthCents int64
	YearCents  int64
}

// mailchimpSkipReasons are the reasons a Mailchimp import skips for.
var mailchimpSkipReasons = []string{SkipInvalidEmail, SkipInvalidRow, SkipSuppressed, SkipNotSubscribed, SkipArchived}

// querier is a pool or a transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// accountLimit is the account's counted contacts and cap, read without a
// lock: lockedCount's numbers, with the count read on every plan.
func accountLimit(ctx context.Context, q querier, accountID string) (contactLimit, string, error) {
	var plan string
	var l contactLimit
	if err := q.QueryRow(ctx, `
		SELECT plan, tester_started_at IS NOT NULL,
			(SELECT count(*) FROM contacts WHERE account_id = a.id AND status IN ('unconfirmed', 'subscribed'))
		FROM accounts a WHERE a.id = $1`, accountID).Scan(&plan, &l.tester, &l.count); err != nil {
		return l, "", fmt.Errorf("read plan: %w", err)
	}
	switch {
	case l.tester:
		l.limit = tester.Contacts
		if plan == "free" {
			l.limit = min(l.limit, FreeContactLimit)
		}
	case plan == "free":
		l.limit = FreeContactLimit
	}
	return l, plan, nil
}

// mcAssessment is what a plan would do in the account as it is now.
type mcAssessment struct {
	limit contactLimit
	plan  string
	first bool
	// existing maps addresses that are contacts to their status.
	existing map[string]string
	// suppressed are the addresses on the suppression list.
	suppressed  map[string]bool
	newByStatus map[string]int
	skipped     map[string]int
	newCounted  int
	// existingTagged and existingSuppressed: see MailchimpPreview.
	existingTagged, existingSuppressed int
	scan                               *fileScan
	holds                              []string
	waits                              bool
}

// assess works out what p would do in the account. q may be the pool, for a
// preview, or the transaction that creates the import.
func (s *Service) assess(ctx context.Context, q querier, accountID string, p *mcPlan) (*mcAssessment, error) {
	a := &mcAssessment{existing: map[string]string{}, suppressed: map[string]bool{},
		newByStatus: map[string]int{StatusSubscribed: 0, StatusUnconfirmed: 0, StatusUnsubscribed: 0, StatusBounced: 0},
		skipped:     map[string]int{}, scan: &fileScan{}}
	for _, r := range mailchimpSkipReasons {
		a.skipped[r] = 0
	}
	var err error
	if a.limit, a.plan, err = accountLimit(ctx, q, accountID); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM imports WHERE account_id = $1 AND status <> 'failed')`,
		accountID).Scan(&a.first); err != nil {
		return nil, err
	}
	var emails, hashes []string
	for _, r := range p.rows {
		if r.Skip == "" {
			emails = append(emails, r.Email)
			hashes = append(hashes, suppressionHash(accountID, r.Email))
		}
	}
	rows, err := q.Query(ctx, `SELECT email, status FROM contacts WHERE account_id = $1 AND email = ANY($2)`, accountID, emails)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e, st string
		if err := rows.Scan(&e, &st); err != nil {
			rows.Close()
			return nil, err
		}
		a.existing[e] = st
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `
		SELECT x.email FROM unnest($2::text[], $3::text[]) AS x(email, hash)
		WHERE EXISTS (SELECT 1 FROM suppressions s WHERE s.account_id = $1 AND s.email_hash = x.hash)`, accountID, emails, hashes)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return nil, err
		}
		a.suppressed[e] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The list checks look at the rows that aren't addresses and at the
	// subscribed part: what would get campaigns.
	seen := map[string]struct{}{}
	for range p.invalid {
		a.scan.addAddress("", true, seen)
	}
	for _, r := range p.rows {
		switch {
		case r.Skip != "":
			a.skipped[r.Skip]++
		case a.existing[r.Email] != "":
			a.existingTagged++
			if Suppressing(r.Status) && Counted(a.existing[r.Email]) {
				a.existingSuppressed++
			}
		case a.suppressed[r.Email]:
			a.skipped[SkipSuppressed]++
		default:
			a.newByStatus[r.Status]++
			if Counted(r.Status) {
				a.newCounted++
				a.scan.addAddress(r.Email, false, seen)
			}
		}
	}
	// Rows the reader skipped without listing them.
	a.skipped[SkipInvalidRow] += p.unlisted

	if a.scan.counts.SharedAddresses, a.scan.counts.SharedAccountID, err = sharedWith(ctx, q, accountID, a.scan.hashes); err != nil {
		return nil, err
	}
	if reason := s.Checks.holdFor(a.scan); reason != "" {
		a.holds = append(a.holds, reason)
	}
	if a.first && a.newCounted > LargeFirstImport {
		a.holds = append(a.holds, HoldLargeFirstImport)
	}
	if a.limit.capped() && a.after() > a.limit.limit {
		a.waits = true
		a.holds = append(a.holds, HoldOverContactLimit)
	}
	return a, nil
}

// after is the account's counted contacts once the import is done.
func (a *mcAssessment) after() int {
	return a.limit.count - a.existingSuppressed + a.newCounted
}

func (s *Service) planPrices() billing.Prices {
	if s.Prices.MonthCents == 0 {
		return billing.DefaultPrices
	}
	return s.Prices
}

// planNeed is the plan the account needs after the import.
func (s *Service) planNeed(a *mcAssessment) PlanNeed {
	n := PlanNeed{Plan: a.plan, Contacts: a.after(), Limit: a.limit.limit, Tester: a.limit.tester}
	if n.Plan == "free" && n.Contacts > FreeContactLimit {
		n.Plan, n.Upgrade = "paid", true
	}
	if n.Plan != "free" {
		p := s.planPrices()
		n.Units = billing.PlanUnits(n.Contacts)
		n.MonthCents, n.YearCents = int64(n.Units)*p.MonthCents, int64(n.Units)*p.YearCents
	}
	return n
}

// PreviewMailchimpImport says what importing the bundle would do, without
// writing anything.
func (s *Service) PreviewMailchimpImport(ctx context.Context, accountID string, b *mailchimp.Bundle) (*MailchimpPreview, error) {
	p, err := planMailchimp(b)
	if err != nil {
		return nil, err
	}
	a, err := s.assess(ctx, s.DB, accountID, p)
	if err != nil {
		return nil, err
	}
	out := &MailchimpPreview{Summary: p.summary, New: a.newByStatus, ExistingTagged: a.existingTagged,
		ExistingSuppressed: a.existingSuppressed, Skipped: a.skipped, Plan: s.planNeed(a),
		Holds: a.holds, Waits: a.waits, TagsToCreate: []string{}, MergeSuppressedExamples: []string{}}
	if out.Holds == nil {
		out.Holds = []string{}
	}
	if len(p.merged) > mergeExamples {
		out.MergeSuppressedExamples = p.merged[:mergeExamples]
	} else if p.merged != nil {
		out.MergeSuppressedExamples = p.merged
	}
	if len(p.tags) > 0 {
		have, err := collect(ctx, s.DB, `SELECT lower(name) FROM tags WHERE account_id = $1 AND lower(name) = ANY($2)`,
			accountID, lowerAll(p.tags))
		if err != nil {
			return nil, err
		}
		for _, t := range p.tags {
			if !have[strings.ToLower(t)] {
				out.TagsToCreate = append(out.TagsToCreate, t)
			}
		}
	}
	return out, nil
}

// MailchimpImportInput confirms a bundle.
type MailchimpImportInput struct {
	Bundle *mailchimp.Bundle
	// FileName is the uploaded file's name, kept in each consent record.
	FileName string
	// PermissionConfirmed says the account has the people's permission to
	// mail them; required, as for a CSV import with subscribed contacts.
	PermissionConfirmed bool
	// IdempotencyKey makes confirming the same bundle again return the
	// import already made. Empty means the bundle's own hash. A key used
	// for a different bundle is refused.
	IdempotencyKey string
}

// CreateMailchimpImport queues a bundle's import. created is false when an
// import with the same key was already made, which is returned unchanged.
func (s *Service) CreateMailchimpImport(ctx context.Context, accountID string, in MailchimpImportInput) (im *Import, created bool, err error) {
	if !in.PermissionConfirmed {
		return nil, false, invalidImport("permission_required", "permission_confirmed",
			"Confirm that these people agreed to get your email. Mailchimp's subscribed contacts come in subscribed, with no confirmation email.")
	}
	if in.Bundle == nil || in.Bundle.Members() == 0 {
		return nil, false, invalidImport("no_rows", "bundle", "The export has no contacts to import.")
	}
	p, err := planMailchimp(in.Bundle)
	if err != nil {
		return nil, false, err
	}
	data, err := p.data()
	if err != nil {
		return nil, false, err
	}
	hash := contentHash(data)
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" {
		key = fmt.Sprintf("sha256:%x", hash)
	}
	if len(key) > 255 {
		return nil, false, invalidImport("invalid_idempotency_key", "idempotency_key", "The idempotency key is at most 255 characters.")
	}
	summary, err := json.Marshal(p.summary)
	if err != nil {
		return nil, false, err
	}
	var fileName *string
	if n := strings.Join(strings.Fields(in.FileName), " "); n != "" {
		n = clip(n, maxFileNameLen)
		fileName = &n
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the account, so two confirms can't both make an import, and two
	// first imports can't both count as first.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, accountID); err != nil {
		return nil, false, fmt.Errorf("lock account: %w", err)
	}
	var id string
	var oldHash []byte
	err = tx.QueryRow(ctx, `SELECT id, content_hash FROM imports WHERE account_id = $1 AND idempotency_key = $2 AND status <> 'failed'`,
		accountID, key).Scan(&id, &oldHash)
	switch {
	case err == nil:
		if string(oldHash) != string(hash) {
			return nil, false, invalidImport("idempotency_key_reused", "idempotency_key",
				"That idempotency key was used to confirm a different export. Use a new key for a new export.")
		}
		im, err := scanImport(tx.QueryRow(ctx, `SELECT `+importColumns+` FROM imports i WHERE i.id = $1`, id))
		return im, false, err
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, false, err
	}

	a, err := s.assess(ctx, tx, accountID, p)
	if err != nil {
		return nil, false, err
	}
	review, holdReason := ReviewNone, (*string)(nil)
	// The limit isn't a review hold yet: the worker decides whether the
	// import waits when it gets to the subscribed part.
	if len(a.holds) > 0 && a.holds[0] != HoldOverContactLimit {
		review, holdReason = ReviewHeld, ptr(a.holds[0])
	}
	c := a.scan.counts
	if err := tx.QueryRow(ctx, `
		INSERT INTO imports (account_id, kind, file_name, permission_confirmed, email_column, csv, total_rows, review, hold_reason,
			created_at, list_source, list_source_detail, invalid_rows, role_addresses, disposable_addresses, shared_addresses,
			shared_account_id, idempotency_key, content_hash, unlisted_skipped, mailchimp)
		VALUES ($1, 'mailchimp', $2, true, 'Email Address', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING id`,
		accountID, fileName, data, len(p.rows), review, holdReason, s.now(), ListSourceExport, mailchimpListDetail,
		c.InvalidRows, c.RoleAddresses, c.DisposableAddresses, c.SharedAddresses, c.SharedAccountID,
		key, hash, p.unlisted, summary).Scan(&id); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO import_address_hashes (import_id, account_id, hash)
		SELECT $1, $2, h FROM unnest($3::bytea[]) AS h ON CONFLICT DO NOTHING`, id, accountID, a.scan.hashes); err != nil {
		return nil, false, fmt.Errorf("store address hashes: %w", err)
	}
	im, err = scanImport(tx.QueryRow(ctx, `SELECT `+importColumns+` FROM imports i WHERE i.id = $1`, id))
	if err != nil {
		return nil, false, err
	}
	return im, true, tx.Commit(ctx)
}

// errWaitForPlan stops a Mailchimp import whose subscribed part would take
// the account past its limit.
var errWaitForPlan = errors.New("import waits for a plan that covers it")

// runMailchimp runs a claimed Mailchimp import from where it got to.
func (s *Service) runMailchimp(ctx context.Context, j *importJob) error {
	rows, err := decodeRows(j.data)
	if err != nil {
		return err
	}
	countedFrom := len(rows)
	for i, r := range rows {
		if r.Skip == "" && Counted(r.Status) {
			countedFrom = i
			break
		}
	}
	checked := false
	for start := j.processed; start < len(rows); {
		if start >= countedFrom && !checked {
			wait, err := s.wouldWait(ctx, j.accountID, rows[countedFrom:])
			if err != nil {
				return err
			}
			if wait {
				return s.waitForPlan(ctx, j)
			}
			checked = true
		}
		end := min(start+importBatch, len(rows))
		if start < countedFrom && end > countedFrom {
			end = countedFrom
		}
		err := s.importMailchimpBatch(ctx, j, rows[start:end], start)
		if errors.Is(err, errWaitForPlan) {
			return s.waitForPlan(ctx, j)
		}
		if err != nil {
			return err
		}
		start = end
	}
	return s.leased(ctx, nil, j, `UPDATE imports SET status = 'done', finished_at = $3, csv = NULL, lease_token = NULL, lease_until = NULL`, s.now())
}

// wouldWait reports whether adding the counted rows' new addresses would
// take a capped account past its limit, counting the existing contacts
// that earlier rows of the import suppressed as gone already.
func (s *Service) wouldWait(ctx context.Context, accountID string, rows []mcRow) (bool, error) {
	l, _, err := accountLimit(ctx, s.DB, accountID)
	if err != nil || !l.capped() {
		return false, err
	}
	emails := make([]string, 0, len(rows))
	hashes := make([]string, 0, len(rows))
	for _, r := range rows {
		emails = append(emails, r.Email)
		hashes = append(hashes, suppressionHash(accountID, r.Email))
	}
	var adding int
	if err := s.DB.QueryRow(ctx, `
		SELECT count(*) FROM unnest($2::text[], $3::text[]) AS e(email, hash)
		WHERE NOT EXISTS (SELECT 1 FROM contacts c WHERE c.account_id = $1 AND c.email = e.email)
		  AND NOT EXISTS (SELECT 1 FROM suppressions s WHERE s.account_id = $1 AND s.email_hash = e.hash)`,
		accountID, emails, hashes).Scan(&adding); err != nil {
		return false, err
	}
	return l.count+adding > l.limit, nil
}

// waitForPlan parks the import until the account's plan has no cap. It's
// held for review as over the contact limit, unless another hold came first.
func (s *Service) waitForPlan(ctx context.Context, j *importJob) error {
	return s.leased(ctx, nil, j, `
		UPDATE imports SET status = 'waiting', lease_token = NULL, lease_until = NULL, attempts = 0,
			hold_reason = CASE WHEN review = 'none' THEN 'over_contact_limit' ELSE hold_reason END,
			review = CASE WHEN review = 'none' THEN 'held' ELSE review END`)
}

type mcExisting struct {
	id, status string
}

// importMailchimpBatch imports rows, which start at row offset of the
// stored data, in one transaction, and records how far the import got.
func (s *Service) importMailchimpBatch(ctx context.Context, j *importJob, rows []mcRow, offset int) error {
	var skips []SkippedRow
	skip := func(i int, r mcRow, reason string) {
		skips = append(skips, SkippedRow{Row: offset + i + 1, Email: clip(r.Email, 254), Reason: reason, Source: r.Ref})
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	limit, err := lockedCount(ctx, tx, j.accountID)
	if err != nil {
		return err
	}
	var emails, hashes []string
	for _, r := range rows {
		if r.Skip == "" {
			emails = append(emails, r.Email)
			hashes = append(hashes, suppressionHash(j.accountID, r.Email))
		}
	}
	existing := map[string]mcExisting{}
	q, err := tx.Query(ctx, `SELECT email, id::text, status FROM contacts WHERE account_id = $1 AND email = ANY($2) FOR UPDATE`,
		j.accountID, emails)
	if err != nil {
		return err
	}
	for q.Next() {
		var e string
		var c mcExisting
		if err := q.Scan(&e, &c.id, &c.status); err != nil {
			q.Close()
			return err
		}
		existing[e] = c
	}
	q.Close()
	if err := q.Err(); err != nil {
		return err
	}
	suppressed, err := collect(ctx, tx, `SELECT email_hash FROM suppressions WHERE account_id = $1 AND email_hash = ANY($2)`, j.accountID, hashes)
	if err != nil {
		return err
	}

	count := limit.count
	var add []mcRow
	var addAt []int                  // each added row's index in rows
	var tagged []candidate           // existing contacts, to gain tags
	contactID := map[string]string{} // email -> contact id, for tags
	var downIDs, downStatuses []string
	existingTagged := 0
	for i, r := range rows {
		if r.Skip != "" {
			skip(i, r, r.Skip)
			continue
		}
		if c, ok := existing[r.Email]; ok {
			existingTagged++
			contactID[r.Email] = c.id
			tagged = append(tagged, candidate{email: r.Email, tags: r.Tags})
			if Suppressing(r.Status) && Counted(c.status) {
				downIDs, downStatuses = append(downIDs, c.id), append(downStatuses, r.Status)
				if limit.capped() {
					count--
				}
			}
			continue
		}
		if suppressed[suppressionHash(j.accountID, r.Email)] {
			skip(i, r, SkipSuppressed)
			continue
		}
		if Counted(r.Status) {
			if limit.capped() && count >= limit.limit {
				// Someone added contacts since the import checked.
				return errWaitForPlan
			}
			count++
		}
		add, addAt = append(add, r), append(addAt, i)
	}

	// As for a CSV import: whether the import is held is read under a
	// share lock, so an admin's approval waits for this batch.
	if err := tx.QueryRow(ctx, `SELECT review FROM imports WHERE id = $1 FOR SHARE`, j.id).Scan(&j.review); err != nil {
		return fmt.Errorf("read import review: %w", err)
	}
	var held *string
	if j.review == ReviewHeld {
		held = &j.id
	}
	now := s.now()
	n := len(add)
	addEmails, firsts, lasts, statuses := make([]string, n), make([]*string, n), make([]*string, n), make([]string, n)
	counted, wordings := make([]bool, n), make([]string, n)
	for i, r := range add {
		addEmails[i], firsts[i], lasts[i], statuses[i] = r.Email, importName(r.First), importName(r.Last), r.Status
		counted[i], wordings[i] = Counted(r.Status), string(r.Consent)
	}
	ins, err := tx.Query(ctx, `
		INSERT INTO contacts (account_id, email, first_name, last_name, status, source, created_at, updated_at, status_changed_at,
			held_import_id, import_id)
		SELECT $1, x.email, x.first_name, x.last_name, x.status, 'import', x.at, x.at, x.at,
			CASE WHEN x.counted THEN $8::uuid END, $9
		FROM (
			SELECT *, $7::timestamptz + (n - 1) * interval '1 microsecond' AS at
			FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::bool[])
				WITH ORDINALITY AS u(email, first_name, last_name, status, counted, n)
		) AS x
		ORDER BY x.n
		ON CONFLICT (account_id, email) DO NOTHING
		RETURNING id::text, email`, j.accountID, addEmails, firsts, lasts, statuses, counted, now, held, j.id)
	if err != nil {
		return err
	}
	added := map[string]string{}
	for ins.Next() {
		var id, e string
		if err := ins.Scan(&id, &e); err != nil {
			ins.Close()
			return err
		}
		added[e] = id
	}
	ins.Close()
	if err := ins.Err(); err != nil {
		return err
	}
	var newIDs, newWordings []string
	var newCands []candidate
	for i, r := range add {
		id := added[r.Email]
		if id == "" {
			// The existing-contacts lock above makes this rare: a contact
			// added since, by another route.
			skip(addAt[i], r, SkipExists)
			continue
		}
		contactID[r.Email] = id
		newIDs, newWordings = append(newIDs, id), append(newWordings, wordings[i])
		newCands = append(newCands, candidate{email: r.Email, tags: r.Tags})
	}
	if len(downIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE contacts c SET status = x.status, status_changed_at = $3, updated_at = $3
			FROM unnest($1::uuid[], $2::text[]) AS x(id, status)
			WHERE c.id = x.id AND c.status IN ('unconfirmed', 'subscribed')`, downIDs, downStatuses, now); err != nil {
			return err
		}
	}
	if len(newIDs) > 0 {
		// Each new contact's consent record: the list source, what
		// Mailchimp knew of its opt-in, and the file it came in.
		if _, err := tx.Exec(ctx, `
			INSERT INTO consents (account_id, contact_id, source, import_id, wording, created_at)
			SELECT $1, x.id, 'import', $3, x.wording::jsonb || jsonb_build_object('file_name', i.file_name), $5
			FROM unnest($2::uuid[], $4::text[]) AS x(id, wording), imports i WHERE i.id = $3`,
			j.accountID, newIDs, j.id, newWordings, j.createdAt); err != nil {
			return err
		}
	}
	if err := s.tagFromCells(ctx, tx, j.accountID, append(newCands, tagged...), contactID, now); err != nil {
		return err
	}
	if err := insertSkips(ctx, tx, j.id, skips); err != nil {
		return err
	}
	processed := offset + len(rows)
	if err := s.leased(ctx, tx, j, `
		UPDATE imports SET processed_rows = $3, imported = imported + $4, skipped = skipped + $5, lease_until = $6,
			existing_tagged = existing_tagged + $7, existing_suppressed = existing_suppressed + $8`,
		processed, len(newIDs), len(skips), now.Add(ImportLease), existingTagged, len(downIDs)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	j.processed = processed
	if afterImportBatch != nil {
		return afterImportBatch(processed)
	}
	return nil
}

// insertSkips records skipped rows.
func insertSkips(ctx context.Context, tx pgx.Tx, importID string, skips []SkippedRow) error {
	if len(skips) == 0 {
		return nil
	}
	lines := make([]int, len(skips))
	emails := make([]string, len(skips))
	reasons := make([]string, len(skips))
	sources := make([]*string, len(skips))
	for i, sk := range skips {
		lines[i], emails[i], reasons[i] = sk.Row, sk.Email, sk.Reason
		if sk.Source != "" {
			sources[i] = &skips[i].Source
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO import_skips (import_id, row_number, email, reason, source_row)
		SELECT $1, x.row_number, x.email, x.reason, x.source_row
		FROM unnest($2::int[], $3::text[], $4::text[], $5::text[]) AS x(row_number, email, reason, source_row)
		ON CONFLICT DO NOTHING`, importID, lines, emails, reasons, sources)
	return err
}
