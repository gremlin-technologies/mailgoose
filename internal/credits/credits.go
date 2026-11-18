// Package credits is the credit ledger (BIL-1, BIL-2, BIL-6).
//
// One credit is one email to one recipient. Every change is a transaction,
// and the balance is their sum, computed in one query and never stored.
// Credits arrive in lots (a free grant or a purchase), each with an expiry;
// debits use the lot that expires first, and record which lots they used in
// credit_allocations, so expiring a lot can take exactly what's left of it.
//
// Free accounts get FreeMonthlyGrant credits when they activate and on each
// monthly anniversary of that; unused free credits expire at the next grant.
// Grants and expiries are applied lazily: every read or debit first catches
// the account up, inside the same transaction and under the account's row
// lock, so no scheduler is needed and two requests can't both apply a grant.
package credits

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FreeMonthlyGrant is what a free account gets each month (BIL-2).
const FreeMonthlyGrant = 1000

// Transaction types.
const (
	TypeGrant    = "grant"
	TypePurchase = "purchase"
	TypeReserve  = "reserve"
	TypeRelease  = "release"
	TypeSpend    = "spend"
	TypeFee      = "fee"
	TypeExpiry   = "expiry"
)

// ErrInsufficientCredits is a debit larger than the balance.
var ErrInsufficientCredits = errors.New("not enough credits")

// ErrNoAccount is an account that doesn't exist.
var ErrNoAccount = errors.New("no such account")

// Beginner starts transactions; *pgxpool.Pool is one.
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Ledger reads and changes accounts' credits.
type Ledger struct {
	DB Beginner
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (l *Ledger) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Transaction is one entry in an account's history.
type Transaction struct {
	ID        int64
	Type      string
	Amount    int64
	ExpiresAt *time.Time
	Ref       string
	CreatedAt time.Time
}

// Summary is an account's balance and when its next free grant comes.
type Summary struct {
	Balance int64
	Plan    string
	// NextGrantAt is when the next free grant arrives; nil on a paid plan or
	// before the account is active.
	NextGrantAt *time.Time
}

// Summary catches the account up and returns its balance.
func (l *Ledger) Summary(ctx context.Context, accountID string) (*Summary, error) {
	var out *Summary
	err := l.inTx(ctx, func(tx pgx.Tx, now time.Time) error {
		acct, err := catchUp(ctx, tx, accountID, now)
		if err != nil {
			return err
		}
		bal, err := balance(ctx, tx, accountID)
		if err != nil {
			return err
		}
		out = &Summary{Balance: bal, Plan: acct.plan}
		if acct.plan == "free" && acct.activatedAt != nil {
			_, next := period(*acct.activatedAt, now)
			out.NextGrantAt = &next
		}
		return nil
	})
	return out, err
}

// History returns up to limit transactions, newest first, older than the
// transaction with ID before (0 for the newest).
func (l *Ledger) History(ctx context.Context, accountID string, before int64, limit int) ([]Transaction, error) {
	var out []Transaction
	err := l.inTx(ctx, func(tx pgx.Tx, now time.Time) error {
		if err := CatchUp(ctx, tx, accountID, now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, type, amount, expires_at, ref, created_at
			FROM credit_transactions
			WHERE account_id = $1 AND ($2 = 0 OR id < $2)
			ORDER BY id DESC
			LIMIT $3`, accountID, before, limit)
		if err != nil {
			return fmt.Errorf("credit history: %w", err)
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Transaction, error) {
			var t Transaction
			err := row.Scan(&t.ID, &t.Type, &t.Amount, &t.ExpiresAt, &t.Ref, &t.CreatedAt)
			return t, err
		})
		return err
	})
	return out, err
}

// Debit takes amount credits from the account as a reserve, spend or fee
// transaction, from the lots that expire first. It fails with
// ErrInsufficientCredits, changing nothing, when the balance is short.
func (l *Ledger) Debit(ctx context.Context, accountID, typ string, amount int64, ref string) (*Transaction, error) {
	var out *Transaction
	err := l.inTx(ctx, func(tx pgx.Tx, now time.Time) error {
		var err error
		out, err = Debit(ctx, tx, accountID, typ, amount, ref, now)
		return err
	})
	return out, err
}

// Release gives back up to amount credits of the reservations with ref, to
// the lots they came from, and returns how many it gave back.
func (l *Ledger) Release(ctx context.Context, accountID, ref string, amount int64) (int64, error) {
	var out int64
	err := l.inTx(ctx, func(tx pgx.Tx, now time.Time) error {
		var err error
		out, err = Release(ctx, tx, accountID, ref, amount, now)
		return err
	})
	return out, err
}

func (l *Ledger) inTx(ctx context.Context, f func(pgx.Tx, time.Time) error) error {
	tx, err := l.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := f(tx, l.now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type account struct {
	plan        string
	activatedAt *time.Time
}

// CatchUp locks the account's row, expires lots past their expiry, and adds
// the current period's free grant if a free, active account hasn't had it.
// Every read and change of credits calls it first, in the same transaction,
// and the row lock it takes is what keeps concurrent debits from
// overdrawing.
func CatchUp(ctx context.Context, tx pgx.Tx, accountID string, now time.Time) error {
	_, err := catchUp(ctx, tx, accountID, now)
	return err
}

func catchUp(ctx context.Context, tx pgx.Tx, accountID string, now time.Time) (*account, error) {
	var a account
	err := tx.QueryRow(ctx, `SELECT plan, activated_at FROM accounts WHERE id = $1 FOR UPDATE`, accountID).
		Scan(&a.plan, &a.activatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, fmt.Errorf("lock account: %w", err)
	}

	// Expire what's left of every lot past its expiry.
	rows, err := tx.Query(ctx, `
		SELECT l.id, l.amount + COALESCE((SELECT sum(c.amount) FROM credit_allocations c WHERE c.lot_id = l.id), 0)
		FROM credit_transactions l
		WHERE l.account_id = $1 AND l.expires_at IS NOT NULL AND l.expires_at <= $2
		ORDER BY l.expires_at, l.id`, accountID, now)
	if err != nil {
		return nil, fmt.Errorf("find expired lots: %w", err)
	}
	type left struct{ lot, n int64 }
	expired, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (left, error) {
		var x left
		err := row.Scan(&x.lot, &x.n)
		return x, err
	})
	if err != nil {
		return nil, fmt.Errorf("find expired lots: %w", err)
	}
	for _, x := range expired {
		if x.n <= 0 {
			continue
		}
		if _, err := insert(ctx, tx, accountID, TypeExpiry, -x.n, nil, "", now, []allocation{{x.lot, -x.n}}); err != nil {
			return nil, err
		}
	}

	if a.plan == "free" && a.activatedAt != nil {
		start, next := period(*a.activatedAt, now)
		var granted bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM credit_transactions
			               WHERE account_id = $1 AND type = 'grant' AND created_at >= $2)`,
			accountID, start).Scan(&granted); err != nil {
			return nil, fmt.Errorf("find grant: %w", err)
		}
		if !granted {
			if _, err := insert(ctx, tx, accountID, TypeGrant, FreeMonthlyGrant, &next, "", now, nil); err != nil {
				return nil, err
			}
		}
	}
	return &a, nil
}

// Debit is Ledger.Debit inside tx.
func Debit(ctx context.Context, tx pgx.Tx, accountID, typ string, amount int64, ref string, now time.Time) (*Transaction, error) {
	if typ != TypeReserve && typ != TypeSpend && typ != TypeFee {
		return nil, fmt.Errorf("credits: %q isn't a debit", typ)
	}
	if amount <= 0 {
		return nil, fmt.Errorf("credits: debit of %d", amount)
	}
	if err := CatchUp(ctx, tx, accountID, now); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT l.id, l.amount + COALESCE((SELECT sum(c.amount) FROM credit_allocations c WHERE c.lot_id = l.id), 0) AS left
		FROM credit_transactions l
		WHERE l.account_id = $1 AND l.expires_at > $2
		ORDER BY l.expires_at, l.id`, accountID, now)
	if err != nil {
		return nil, fmt.Errorf("find lots: %w", err)
	}
	lots, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (allocation, error) {
		var a allocation
		err := row.Scan(&a.lot, &a.amount)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("find lots: %w", err)
	}
	var allocs []allocation
	need := amount
	for _, lot := range lots {
		if need == 0 {
			break
		}
		if lot.amount <= 0 {
			continue
		}
		take := min(need, lot.amount)
		allocs = append(allocs, allocation{lot.lot, -take})
		need -= take
	}
	if need > 0 {
		return nil, ErrInsufficientCredits
	}
	return insert(ctx, tx, accountID, typ, -amount, nil, ref, now, allocs)
}

// Release is Ledger.Release inside tx. Credits go back to the lots that
// expire last first, so they stay usable longest.
func Release(ctx context.Context, tx pgx.Tx, accountID, ref string, amount int64, now time.Time) (int64, error) {
	if ref == "" || amount <= 0 {
		return 0, fmt.Errorf("credits: release of %d for %q", amount, ref)
	}
	if err := CatchUp(ctx, tx, accountID, now); err != nil {
		return 0, err
	}
	// What each lot still has reserved under ref: reserved minus released.
	rows, err := tx.Query(ctx, `
		SELECT c.lot_id, -sum(c.amount)
		FROM credit_allocations c
		JOIN credit_transactions t ON t.id = c.transaction_id
		JOIN credit_transactions l ON l.id = c.lot_id
		WHERE t.account_id = $1 AND t.ref = $2 AND t.type IN ('reserve', 'release')
		GROUP BY c.lot_id, l.expires_at
		HAVING -sum(c.amount) > 0
		ORDER BY l.expires_at DESC, c.lot_id DESC`, accountID, ref)
	if err != nil {
		return 0, fmt.Errorf("find reservation: %w", err)
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (allocation, error) {
		var a allocation
		err := row.Scan(&a.lot, &a.amount)
		return a, err
	})
	if err != nil {
		return 0, fmt.Errorf("find reservation: %w", err)
	}
	var allocs []allocation
	var total int64
	for _, h := range held {
		give := min(amount-total, h.amount)
		if give <= 0 {
			break
		}
		allocs = append(allocs, allocation{h.lot, give})
		total += give
	}
	if total == 0 {
		return 0, nil
	}
	if _, err := insert(ctx, tx, accountID, TypeRelease, total, nil, ref, now, allocs); err != nil {
		return 0, err
	}
	// A lot that expired while reserved expires again now that it's back.
	if err := CatchUp(ctx, tx, accountID, now); err != nil {
		return 0, err
	}
	return total, nil
}

type allocation struct {
	lot    int64
	amount int64
}

func insert(ctx context.Context, tx pgx.Tx, accountID, typ string, amount int64, expires *time.Time, ref string, now time.Time, allocs []allocation) (*Transaction, error) {
	t := &Transaction{Type: typ, Amount: amount, ExpiresAt: expires, Ref: ref}
	err := tx.QueryRow(ctx, `
		INSERT INTO credit_transactions (account_id, type, amount, expires_at, ref, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`, accountID, typ, amount, expires, ref, now).Scan(&t.ID, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("add %s: %w", typ, err)
	}
	for _, a := range allocs {
		if _, err := tx.Exec(ctx, `INSERT INTO credit_allocations (transaction_id, lot_id, amount) VALUES ($1, $2, $3)`,
			t.ID, a.lot, a.amount); err != nil {
			return nil, fmt.Errorf("allocate %s: %w", typ, err)
		}
	}
	return t, nil
}

// balance is the account's credits: the sum of its transactions.
func balance(ctx context.Context, tx pgx.Tx, accountID string) (int64, error) {
	var b int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount), 0) FROM credit_transactions WHERE account_id = $1`,
		accountID).Scan(&b); err != nil {
		return 0, fmt.Errorf("balance: %w", err)
	}
	return b, nil
}

// period returns the start of the monthly period containing now, counted
// from anchor (the account's activation), and the start of the next one. A
// period that starts on the 31st starts on the last day of shorter months.
func period(anchor, now time.Time) (start, next time.Time) {
	anchor, now = anchor.UTC(), now.UTC()
	k := (now.Year()-anchor.Year())*12 + int(now.Month()-anchor.Month())
	if k < 0 {
		k = 0
	}
	for k > 0 && addMonths(anchor, k).After(now) {
		k--
	}
	for !addMonths(anchor, k+1).After(now) {
		k++
	}
	return addMonths(anchor, k), addMonths(anchor, k+1)
}

// addMonths is t plus k calendar months, keeping the day of the month but
// clamping it to the month's last day (Jan 31 + 1 month = Feb 28).
func addMonths(t time.Time, k int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(k), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(d, last)-1)
}
