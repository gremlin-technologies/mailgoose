package sending

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
)

func TestSliceSize(t *testing.T) {
	for _, c := range []struct {
		s       Slicing
		n, want int
	}{
		{Slicing{}, 0, 0},
		{Slicing{}, 150, 0},
		{Slicing{}, 200, 0},   // 200 or fewer go out whole
		{Slicing{}, 201, 200}, // at least 200
		{Slicing{}, 2000, 200},
		{Slicing{}, 2001, 201}, // 10%, rounded up
		{Slicing{}, 5000, 500},
		{Slicing{}, 1_000_000, 100_000},
		{Slicing{Percent: 25, Min: 50}, 50, 0},
		{Slicing{Percent: 25, Min: 50}, 51, 50},
		{Slicing{Percent: 25, Min: 50}, 1000, 250},
		{Slicing{Percent: 100, Min: 1}, 10, 0}, // never the whole list
	} {
		if got := c.s.Size(c.n); got != c.want {
			t.Errorf("%+v of %d: %d, want %d", c.s, c.n, got, c.want)
		}
	}
}

// slicedHarness launches h's campaign to Sam and n more from an account
// made at created, with launched earlier campaigns.
func slicedHarness(t *testing.T, n int, created time.Time, launched int) *harness {
	t.Helper()
	h := newHarness(t)
	if launched > 0 {
		if _, err := h.db.Exec(context.Background(), `
			INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at)
			SELECT $1, 'Earlier ' || i, true, 'sent', $2 FROM generate_series(1, $3) i`,
			h.acc, created, launched); err != nil {
			t.Fatal(err)
		}
	}
	return launchTo(t, h, n, created)
}

// slice reads the campaign's slice, and how many recipients are in it.
func (h *harness) slice(t *testing.T) (*campaigns.Slice, int) {
	t.Helper()
	c, err := h.svc.Campaigns.Get(context.Background(), h.acc, h.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	var in int
	if err := h.db.QueryRow(context.Background(), `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND in_slice`,
		h.campaign.ID).Scan(&in); err != nil {
		t.Fatal(err)
	}
	return c.Slice, in
}

func TestSliceOnlyForNewAccounts(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name     string
		n        int
		created  time.Time
		launched int
		want     int
	}{
		{"new account", 999, now.AddDate(0, 0, -2), 0, 200},
		{"bigger list: 10%", 2999, now.AddDate(0, 0, -2), 0, 300},
		{"200 go out whole", 199, now.AddDate(0, 0, -2), 0, 0},
		{"201 slice at 200", 200, now.AddDate(0, 0, -2), 0, 200},
		{"past 30 days, third campaign", 999, now.AddDate(0, 0, -40), 2, 200},
		{"in 30 days, past 3 campaigns", 999, now.AddDate(0, 0, -10), 5, 200},
		{"established", 999, now.AddDate(0, 0, -30), 3, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := slicedHarness(t, c.n, c.created, c.launched)
			sl, in := h.slice(t)
			switch {
			case c.want == 0 && (sl != nil || in != 0):
				t.Errorf("sliced: %+v, %d in it", sl, in)
			case c.want > 0 && (sl == nil || sl.Size != c.want || sl.State != SliceSending || sl.ReleaseAt != nil || in != c.want):
				t.Errorf("slice %+v, %d in it, want %d", sl, in, c.want)
			}
		})
	}
}

// sendSlice dispatches and sends until nothing more goes, and returns how
// many were sent.
func sendSlice(t *testing.T, h *harness, p *Pipeline, q *recorded) int {
	t.Helper()
	n := 0
	for range 5 {
		dispatch(t, p, q)
		batches := q.take()
		if len(batches) == 0 {
			break
		}
		for _, b := range batches {
			n += len(b.IDs)
			if err := p.HandleBatch(context.Background(), b, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	return n
}

func TestSliceReleasedWhenClean(t *testing.T) {
	h := slicedHarness(t, 999, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 0)
	p := h.pipeline(fastLimits)
	q := &recorded{}

	// Only the slice goes, then the rest waits an hour.
	if n := sendSlice(t, h, p, q); n != 200 {
		t.Fatalf("sent %d, want the slice of 200", n)
	}
	var notSlice int
	if err := h.db.QueryRow(context.Background(), `
		SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND status = 'sent' AND NOT in_slice`,
		h.campaign.ID).Scan(&notSlice); err != nil || notSlice != 0 {
		t.Fatalf("%d sent outside the slice, %v", notSlice, err)
	}
	sl, _ := h.slice(t)
	if sl.State != SliceHold || sl.ReleaseAt == nil || !sl.ReleaseAt.Equal(h.now.Add(time.Hour)) || h.status(t) != campaigns.StatusSending {
		t.Fatalf("after the slice: %+v, %s", sl, h.status(t))
	}
	h.now = h.now.Add(59 * time.Minute)
	if n := sendSlice(t, h, p, q); n != 0 {
		t.Fatalf("sent %d before the wait was over", n)
	}

	// One complaint in 200 is 0.5%: over the limits that pause. One hard
	// bounce isn't (0.5% of 5%), so the rest goes.
	h.eventsFor(t, "bounce", "permanent/general", 1, h.now)
	h.now = h.now.Add(2 * time.Minute)
	if n := sendSlice(t, h, p, q); n != 800 {
		t.Fatalf("released %d, want the other 800", n)
	}
	if sl, _ := h.slice(t); sl.State != SliceReleased || sl.ReleaseAt != nil {
		t.Errorf("after release: %+v", sl)
	}
	dispatch(t, p, q)
	if h.status(t) != campaigns.StatusSent || len(h.fake.Sent()) != 1000 {
		t.Errorf("status %s, %d sent", h.status(t), len(h.fake.Sent()))
	}
}

func TestSliceStoppedWhenOverPauseLimits(t *testing.T) {
	h := slicedHarness(t, 999, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 0)
	var told []Health
	p := h.pipeline(fastLimits)
	p.Health = func(_ context.Context, hl Health) { told = append(told, hl) }
	q := &recorded{}
	if n := sendSlice(t, h, p, q); n != 200 {
		t.Fatalf("sent %d", n)
	}
	before := h.balance(t)
	// One complaint: 0.5% of the slice, over 0.3%, though 200 copies are
	// under the 500 the usual check needs.
	h.eventsFor(t, "complaint", "", 1, h.now)
	h.now = h.now.Add(time.Hour)
	if n := sendSlice(t, h, p, q); n != 0 {
		t.Fatalf("sent %d of the rest", n)
	}
	sl, _ := h.slice(t)
	if paused, reason := h.paused(t); !paused || reason == "" || sl.State != SliceStopped || h.status(t) != campaigns.StatusCancelled {
		t.Fatalf("paused %v (%q), slice %+v, status %s", paused, reason, sl, h.status(t))
	}
	if c := h.counts(t); c["sent"] != 200 || c["skipped"] != 800 {
		t.Errorf("rows %v", c)
	}
	if after := h.balance(t); after != before+800 {
		t.Errorf("balance %d, want %d: the rest refunded", after, before+800)
	}
	var logged int
	if err := h.db.QueryRow(context.Background(), `SELECT count(*) FROM admin_actions WHERE account_id = $1 AND action = 'auto_pause'`,
		h.acc).Scan(&logged); err != nil || logged != 1 {
		t.Errorf("auto_pause logged %d, %v", logged, err)
	}
	if len(told) != 1 || told[0].Pause == nil || told[0].Pause.Rates != (Rates{Sent: 200, Complaints: 1}) {
		t.Errorf("told %+v", told)
	}
	var open int
	if err := h.db.QueryRow(context.Background(), `SELECT count(*) FROM send_queue WHERE campaign_id = $1 AND done_at IS NULL`,
		h.campaign.ID).Scan(&open); err != nil || open != 0 {
		t.Errorf("%d queue rows open, %v", open, err)
	}
}

func TestSliceHeldWhenOverWarning(t *testing.T) {
	h := slicedHarness(t, 999, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 0)
	var told []Health
	p := h.pipeline(fastLimits)
	p.Health = func(_ context.Context, hl Health) { told = append(told, hl) }
	q := &recorded{}
	if n := sendSlice(t, h, p, q); n != 200 {
		t.Fatalf("sent %d", n)
	}
	// Five hard bounces: 2.5%, over the 2% early warning, under the 5% pause.
	h.eventsFor(t, "bounce", "permanent/general", 5, h.now)
	h.now = h.now.Add(time.Hour)
	if n := sendSlice(t, h, p, q); n != 0 {
		t.Fatalf("sent %d of the rest", n)
	}
	sl, _ := h.slice(t)
	until := h.now.Add(ThrottleFor)
	if sl.State != SliceHeld || sl.ReleaseAt == nil || !sl.ReleaseAt.Equal(until) || h.status(t) != campaigns.StatusSending {
		t.Fatalf("slice %+v, status %s", sl, h.status(t))
	}
	if paused, _ := h.paused(t); paused {
		t.Error("paused")
	}
	if len(told) != 1 || told[0].Throttle == nil || !told[0].Throttle.Until.Equal(until) {
		t.Fatalf("told %+v", told)
	}
	lim, err := p.Limits.Account(context.Background(), h.db, h.acc, h.now)
	if err != nil || lim.LoweredUntil == nil {
		t.Errorf("limit %+v, %v", lim, err)
	}

	// Once the throttle lifts, the slice is checked again; still under the
	// pause limits, the rest goes (a day's worth at a time).
	h.now = until.Add(time.Minute)
	if n := sendSlice(t, h, p, q); n != 800 {
		t.Fatalf("released %d, want 800", n)
	}
	if sl, _ := h.slice(t); sl.State != SliceReleased {
		t.Errorf("slice %+v", sl)
	}
}

func TestSliceHeldThenStopped(t *testing.T) {
	h := slicedHarness(t, 999, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 0)
	p := h.pipeline(fastLimits)
	q := &recorded{}
	sendSlice(t, h, p, q)
	h.eventsFor(t, "bounce", "permanent/general", 5, h.now)
	h.now = h.now.Add(time.Hour)
	sendSlice(t, h, p, q)
	// More bounces come in while it's held: 11 of 200 is over 5%.
	if _, err := h.db.Exec(context.Background(), `DELETE FROM send_events WHERE campaign_id = $1`, h.campaign.ID); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "bounce", "permanent/general", 11, h.now)
	h.now = h.now.Add(ThrottleFor + time.Minute)
	if n := sendSlice(t, h, p, q); n != 0 {
		t.Fatalf("sent %d", n)
	}
	if sl, _ := h.slice(t); sl.State != SliceStopped || h.status(t) != campaigns.StatusCancelled {
		t.Errorf("slice %+v, status %s", sl, h.status(t))
	}
	if paused, _ := h.paused(t); !paused {
		t.Error("not paused")
	}
}

func TestSliceCancelWhileWaiting(t *testing.T) {
	h := slicedHarness(t, 999, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 0)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	q := &recorded{}

	// While the slice is going out, it can't be cancelled.
	dispatch(t, p, q)
	var st *campaigns.StatusError
	if _, err := h.svc.Cancel(ctx, h.acc, h.campaign.ID); !errors.As(err, &st) {
		t.Fatalf("cancel while sending the slice: %v", err)
	}
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	dispatch(t, p, q)
	before := h.balance(t)
	c, err := h.svc.Cancel(ctx, h.acc, h.campaign.ID)
	if err != nil || c.Status != campaigns.StatusCancelled || c.Slice == nil || c.Slice.ReleaseAt != nil ||
		c.Slice.State != SliceCancelled {
		t.Fatalf("cancel while waiting: %+v, %v", c, err)
	}
	if after := h.balance(t); after != before+800 {
		t.Errorf("balance %d, want %d", after, before+800)
	}
	h.now = h.now.Add(2 * time.Hour)
	if n := sendSlice(t, h, p, q); n != 0 {
		t.Errorf("sent %d after the cancel", n)
	}
}

// TestSliceStoppedWhileSending checks a campaign stopped while its slice
// goes out ends with its slice cancelled, not still waiting.
func TestSliceStoppedWhileSending(t *testing.T) {
	h := slicedHarness(t, 999, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 0)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	if got := h.sliceState(t); got != SliceSending {
		t.Fatalf("slice state %q before the stop", got)
	}
	c, err := h.svc.Stop(ctx, h.acc, h.campaign.ID)
	if err != nil || c.Status != campaigns.StatusCancelled || c.Slice == nil || c.Slice.State != SliceCancelled || c.Slice.ReleaseAt != nil {
		t.Fatalf("stop while sending the slice: %+v, %v", c, err)
	}
	// Batches already handed to the sender finish, as with any stop.
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	h.now = h.now.Add(2 * time.Hour)
	if n := sendSlice(t, h, p, q); n != 0 {
		t.Errorf("sent %d after the stop", n)
	}
	if got := h.sliceState(t); got != SliceCancelled {
		t.Errorf("slice state %q after the stop", got)
	}
}

// TestSliceReleasedKeepsStateOnStop checks a stop only marks a slice that was
// still waiting: one already released keeps saying so.
func TestSliceReleasedKeepsStateOnStop(t *testing.T) {
	h := slicedHarness(t, 999, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 0)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `UPDATE campaigns SET slice_state = $2 WHERE id = $1`, h.campaign.ID, SliceReleased); err != nil {
		t.Fatal(err)
	}
	c, err := h.svc.Stop(ctx, h.acc, h.campaign.ID)
	if err != nil || c.Slice == nil || c.Slice.State != SliceReleased {
		t.Fatalf("stop after the rest was released: %+v, %v", c, err)
	}
}

// sliceState is the campaign's slice_state, or "" if it has none.
func (h *harness) sliceState(t *testing.T) string {
	t.Helper()
	var s *string
	if err := h.db.QueryRow(context.Background(), `SELECT slice_state FROM campaigns WHERE id = $1`, h.campaign.ID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return deref(s)
}

func TestSliceScheduled(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	at := h.now.Add(48 * time.Hour)
	c, err := h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{SendAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	h.campaign = c
	launchTo(t, h, 999, h.now.AddDate(0, 0, -1))
	if h.status(t) != campaigns.StatusScheduled {
		t.Fatalf("status %s", h.status(t))
	}
	p := h.pipeline(fastLimits)
	q := &recorded{}
	if n := sendSlice(t, h, p, q); n != 0 {
		t.Fatalf("sent %d before its time", n)
	}
	h.now = at
	if n := sendSlice(t, h, p, q); n != 200 {
		t.Errorf("sent %d at its time, want the slice of 200", n)
	}
	if sl, _ := h.slice(t); sl == nil || sl.State != SliceHold {
		t.Errorf("slice %+v", sl)
	}
}
