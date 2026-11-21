package sending

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Table sizes (MG-95). Every copy sent is one campaign_recipients row and
// one or two send_events rows, kept 12 months, so those two tables are where
// storage grows first: RDS storage, then the 30-day account queries.
// `mailgoose admin storage` lists them first, then the largest other tables,
// and the daily digest gives one line with their sizes and how much they
// grew over 7 days.
//
// Sizes are pg_total_relation_size (the table with its indexes and TOAST).
// Row counts are the planner's estimate, pg_class.reltuples, which is
// cheap but only as fresh as the last ANALYZE (autovacuum's or anyone's);
// a table never analysed has none. The 7-day change needs a size from 7
// days before: the digest job writes every table's size to
// table_size_snapshots once a day, when it claims the day, and deletes rows
// older than SnapshotKeepDays.

// TrackedTables are the tables storage growth is watched by, listed first
// in this order.
var TrackedTables = []string{"campaign_recipients", "send_events"}

// Storage report limits.
const (
	// DefaultStorageTables is how many tables the report lists when no
	// limit is asked for, the tracked tables included.
	DefaultStorageTables = 10
	// MaxStorageTables is the most it lists at once.
	MaxStorageTables = 50
	// StorageCompareDays is how far back the change is measured.
	StorageCompareDays = 7
	// SnapshotKeepDays is how long a day's sizes are kept.
	SnapshotKeepDays = 56
)

// TableSize is one table's size.
type TableSize struct {
	Name string
	// RowEstimate is the planner's row estimate; nil when the table has
	// never been analysed.
	RowEstimate *int64
	// TotalBytes is its size with indexes and TOAST.
	TotalBytes int64
	// BytesThen is its size in the snapshot of the compare day; nil when
	// there's no snapshot of it that day.
	BytesThen *int64
}

// Change is how much the table grew since the compare day (negative when
// it shrank); nil with no snapshot to compare with.
func (t TableSize) Change() *int64 {
	if t.BytesThen == nil {
		return nil
	}
	c := t.TotalBytes - *t.BytesThen
	return &c
}

// StorageReport is the tables' sizes.
type StorageReport struct {
	// Tables are the tracked tables first, then the largest others, at
	// most the limit asked for.
	Tables []TableSize
	// TotalTables counts every table in the schema; TotalBytes is their
	// sizes added up.
	TotalTables int
	TotalBytes  int64
	// CompareDay is the UTC day whose snapshot BytesThen comes from.
	CompareDay time.Time
}

// compareDay is the UTC day StorageCompareDays before now's.
func compareDay(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d-StorageCompareDays, 0, 0, 0, 0, time.UTC)
}

// tableSizes is every ordinary table in the schema the connection uses
// (current_schema(): public in production, a test's own schema in tests).
const tableSizes = `
	SELECT c.relname, CASE WHEN c.reltuples < 0 THEN NULL ELSE c.reltuples::bigint END,
		pg_total_relation_size(c.oid)
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = current_schema() AND c.relkind = 'r'`

// StorageSizes reports the tables' sizes now, with each one's size on the
// compare day where a snapshot has it: at most limit (1 to
// MaxStorageTables) tables, the tracked ones first.
func StorageSizes(ctx context.Context, q digestQuerier, now time.Time, limit int) (StorageReport, error) {
	limit = min(max(limit, 1), MaxStorageTables)
	rep := StorageReport{CompareDay: compareDay(now)}
	rows, err := q.Query(ctx, `
		SELECT t.relname, t.reltuples, t.size, s.total_bytes
		FROM (`+tableSizes+`) t (relname, reltuples, size)
		LEFT JOIN table_size_snapshots s ON s.day = $1 AND s.table_name = t.relname`, rep.CompareDay)
	if err != nil {
		return rep, err
	}
	var all []TableSize
	for rows.Next() {
		var t TableSize
		if err := rows.Scan(&t.Name, &t.RowEstimate, &t.TotalBytes, &t.BytesThen); err != nil {
			rows.Close()
			return rep, err
		}
		all = append(all, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}
	rank := func(name string) int {
		if i := slices.Index(TrackedTables, name); i >= 0 {
			return i
		}
		return len(TrackedTables)
	}
	slices.SortFunc(all, func(a, b TableSize) int {
		return cmp.Or(cmp.Compare(rank(a.Name), rank(b.Name)), cmp.Compare(b.TotalBytes, a.TotalBytes), strings.Compare(a.Name, b.Name))
	})
	rep.TotalTables = len(all)
	for _, t := range all {
		rep.TotalBytes += t.TotalBytes
	}
	rep.Tables = all[:min(limit, len(all))]
	return rep, nil
}

// RecordTableSizes writes every table's size now as now's UTC day's
// snapshot, unless that day has one, and deletes snapshots older than
// SnapshotKeepDays.
func RecordTableSizes(ctx context.Context, q querier, now time.Time) error {
	now = now.UTC()
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if _, err := q.Exec(ctx, `
		INSERT INTO table_size_snapshots (day, table_name, row_estimate, total_bytes, taken_at)
		SELECT $1::date, t.relname, t.reltuples, t.size, $2::timestamptz
		FROM (`+tableSizes+`) t (relname, reltuples, size)
		ON CONFLICT (day, table_name) DO NOTHING`, day, now); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM table_size_snapshots WHERE day < $1`, day.AddDate(0, 0, -SnapshotKeepDays))
	return err
}

// storageLine is the digest's line on the tracked tables: "Storage:
// campaign_recipients 1.2 GB (+80 MB in 7 days), send_events 640 MB (no
// size from 7 days ago to compare). ...".
func storageLine(rep StorageReport) string {
	var parts []string
	for _, t := range rep.Tables {
		if !slices.Contains(TrackedTables, t.Name) {
			continue
		}
		change := "no size from 7 days ago to compare"
		if c := t.Change(); c != nil {
			change = changeText(*c) + " in 7 days"
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s)", t.Name, SizeText(t.TotalBytes), change))
	}
	if len(parts) == 0 {
		return "Storage: no sizes found. `mailgoose admin storage` lists the largest tables."
	}
	return "Storage: " + strings.Join(parts, ", ") + ". `mailgoose admin storage` lists the largest tables."
}

// SizeText writes n bytes the way Postgres's pg_size_pretty does, in
// powers of 1,024, with a decimal under 10: "512 bytes", "16 KB", "1.2 GB".
func SizeText(n int64) string {
	if n < 0 {
		return "-" + SizeText(-n)
	}
	if n < 1024 {
		if n == 1 {
			return "1 byte"
		}
		return fmt.Sprintf("%d bytes", n)
	}
	v := float64(n)
	unit := ""
	for _, u := range []string{"KB", "MB", "GB", "TB", "PB"} {
		v /= 1024
		unit = u
		if v < 1024 {
			break
		}
	}
	if v < 10 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + " " + unit
	}
	return fmt.Sprintf("%.0f %s", v, unit)
}

// changeText is a change in size: "+80 MB", "-3.5 MB" or "no change".
func changeText(n int64) string {
	switch {
	case n == 0:
		return "no change"
	case n > 0:
		return "+" + SizeText(n)
	}
	return SizeText(n)
}
