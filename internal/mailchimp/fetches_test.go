package mailchimp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fetchJob is a FetchJob for a new account connected to a fake Mailchimp
// API holding the bakery, saving each fetch straight to the store.
func fetchJob(t *testing.T) (*FetchJob, *fakeAPI, string) {
	t.Helper()
	conns, _, acct := connections(t)
	api := newFakeAPI(t)
	bakery(api)
	conns.APIURL = api.URL
	if _, err := conns.Put(t.Context(), acct, fakeToken, "us21", "Riverside Bakery"); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: conns.DB}
	j := &FetchJob{Store: s, Connections: conns, Save: func(ctx context.Context, accountID string, f *Fetched) (string, error) {
		u, err := s.SaveFetched(ctx, accountID, f.Data, []byte(`{}`), Options{GroupColumns: f.GroupColumns})
		if err != nil {
			return "", err
		}
		return u.ID, nil
	}}
	return j, api, acct
}

func TestFetchJobMakesABundle(t *testing.T) {
	j, _, acct := fetchJob(t)
	ctx := t.Context()
	f, err := j.Store.QueueFetch(ctx, acct, FetchRequest{AudienceIDs: []string{"a1b2c3"}, SegmentIDs: []string{"40213"}, Campaigns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != FetchQueued || f.Campaigns != 1 {
		t.Fatalf("queued = %+v", f)
	}
	if _, err := j.Store.QueueFetch(ctx, acct, FetchRequest{AudienceIDs: []string{"a1b2c3"}}); !errors.Is(err, ErrFetchRunning) {
		t.Fatalf("second fetch: %v, want ErrFetchRunning", err)
	}
	if ran, err := j.RunNext(ctx); !ran || err != nil {
		t.Fatalf("RunNext = %v, %v", ran, err)
	}
	if ran, err := j.RunNext(ctx); ran || err != nil {
		t.Fatalf("second RunNext = %v, %v", ran, err)
	}
	got, err := j.Store.GetFetch(ctx, acct, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != FetchDone || got.BundleID == nil || got.MembersRead != 2506 || got.FinishedAt == nil || got.ErrorCode != nil {
		t.Fatalf("done = %+v", got)
	}
	u, err := j.Store.Get(ctx, acct, *got.BundleID)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Fetched {
		t.Error("the bundle isn't marked fetched")
	}
	if _, err := j.Store.GetFetch(ctx, "00000000-0000-0000-0000-000000000000", f.ID); !errors.Is(err, ErrFetchNotFound) {
		t.Errorf("another account's fetch: %v", err)
	}
	// With that one finished, the account can fetch again.
	if _, err := j.Store.QueueFetch(ctx, acct, FetchRequest{AudienceIDs: []string{"a1b2c3"}}); err != nil {
		t.Errorf("fetching again: %v", err)
	}
}

func TestFetchJobFailsWithAReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		req    FetchRequest
		setup  func(*FetchJob, *fakeAPI, string)
		code   string
		forgot bool
	}{
		{name: "unknown audience", req: FetchRequest{AudienceIDs: []string{"nope"}}, code: ErrUnknownAudience},
		{name: "revoked", req: FetchRequest{AudienceIDs: []string{"a1b2c3"}}, code: FetchErrRevoked, forgot: true,
			setup: func(_ *FetchJob, api *fakeAPI, _ string) { api.revokeAfter.Store(1) }},
		{name: "not connected", req: FetchRequest{AudienceIDs: []string{"a1b2c3"}}, code: FetchErrNotConnected, forgot: true,
			setup: func(j *FetchJob, _ *fakeAPI, acct string) {
				if err := j.Connections.Forget(context.Background(), acct); err != nil {
					panic(err)
				}
			}},
		{name: "too many members", req: FetchRequest{AudienceIDs: []string{"a1b2c3"}}, code: ErrTooManyMembers,
			setup: func(j *FetchJob, _ *fakeAPI, _ string) { j.MaxMembers = 10 }},
		{name: "save refused", req: FetchRequest{AudienceIDs: []string{"a1b2c3"}}, code: FetchErrTooMany,
			setup: func(j *FetchJob, _ *fakeAPI, _ string) {
				j.Save = func(context.Context, string, *Fetched) (string, error) { return "", ErrTooManyUploads }
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, api, acct := fetchJob(t)
			ctx := t.Context()
			if tc.setup != nil {
				tc.setup(j, api, acct)
			}
			f, err := j.Store.QueueFetch(ctx, acct, tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if ran, err := j.RunNext(ctx); !ran || err != nil {
				t.Fatalf("RunNext = %v, %v", ran, err)
			}
			got, err := j.Store.GetFetch(ctx, acct, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != FetchFailed || got.ErrorCode == nil || *got.ErrorCode != tc.code || got.ErrorMessage == nil || got.BundleID != nil {
				t.Fatalf("fetch = %+v, code %v", got, deref(got.ErrorCode))
			}
			conn, err := j.Connections.Get(ctx, acct)
			if err != nil {
				t.Fatal(err)
			}
			if (conn == nil) != tc.forgot {
				t.Errorf("connection kept = %v, want forgotten %v", conn != nil, tc.forgot)
			}
		})
	}
}

func TestFetchJobGivesUpOnAFetchThatKeepsStopping(t *testing.T) {
	j, _, acct := fetchJob(t)
	ctx := t.Context()
	f, err := j.Store.QueueFetch(ctx, acct, FetchRequest{AudienceIDs: []string{"a1b2c3"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Store.DB.Exec(ctx, `UPDATE mailchimp_fetches SET status = 'running', attempts = $2,
		lease_token = gen_random_uuid(), lease_until = now() - interval '1 minute' WHERE id = $1`, f.ID, maxFetchAttempts); err != nil {
		t.Fatal(err)
	}
	if ran, err := j.RunNext(ctx); ran || err != nil {
		t.Fatalf("RunNext = %v, %v", ran, err)
	}
	got, err := j.Store.GetFetch(ctx, acct, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != FetchFailed || deref(got.ErrorCode) != FetchErrStopped {
		t.Fatalf("fetch = %+v", got)
	}
}

func TestPruneDropsOldFetches(t *testing.T) {
	j, _, acct := fetchJob(t)
	ctx := t.Context()
	f, err := j.Store.QueueFetch(ctx, acct, FetchRequest{AudienceIDs: []string{"nope"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.RunNext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Store.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Store.GetFetch(ctx, acct, f.ID); err != nil {
		t.Fatalf("a new failed fetch was pruned: %v", err)
	}
	j.Store.Now = func() time.Time { return time.Now().Add(FetchTTL + time.Hour) }
	if _, err := j.Store.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Store.GetFetch(ctx, acct, f.ID); !errors.Is(err, ErrFetchNotFound) {
		t.Fatalf("an old fetch wasn't pruned: %v", err)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
