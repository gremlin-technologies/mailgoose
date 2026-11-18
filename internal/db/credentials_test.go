package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSource struct {
	creds []Credentials
	calls int
	err   error
}

func (f *fakeSource) Fetch(context.Context) (Credentials, error) {
	f.calls++
	if f.err != nil {
		return Credentials{}, f.err
	}
	return f.creds[min(f.calls, len(f.creds))-1], nil
}

func TestRotatingCredentialsCachesAfterSuccess(t *testing.T) {
	src := &fakeSource{creds: []Credentials{{"u", "p1"}}}
	r := newRotatingCredentials(src, time.Hour)
	ctx := context.Background()

	for range 3 {
		c, err := r.beforeConnect(ctx)
		if err != nil || c.Password != "p1" {
			t.Fatalf("beforeConnect = %v, %v", c, err)
		}
		r.afterConnect()
	}
	if src.calls != 1 {
		t.Errorf("fetched %d times, want 1", src.calls)
	}
}

func TestRotatingCredentialsRefetchAfterFailedConnect(t *testing.T) {
	src := &fakeSource{creds: []Credentials{{"u", "old"}, {"u", "new"}}}
	r := newRotatingCredentials(src, time.Hour)
	ctx := context.Background()

	if c, _ := r.beforeConnect(ctx); c.Password != "old" {
		t.Fatalf("first password = %q", c.Password)
	}
	// No afterConnect: the attempt failed, as after a rotation.
	c, err := r.beforeConnect(ctx)
	if err != nil || c.Password != "new" {
		t.Fatalf("after a failed connect got %v, %v; want the new password", c, err)
	}
	if src.calls != 2 {
		t.Errorf("fetched %d times, want 2", src.calls)
	}
}

func TestRotatingCredentialsMaxAge(t *testing.T) {
	src := &fakeSource{creds: []Credentials{{"u", "p1"}, {"u", "p2"}}}
	r := newRotatingCredentials(src, time.Minute)
	now := time.Unix(0, 0)
	r.now = func() time.Time { return now }
	ctx := context.Background()

	_, _ = r.beforeConnect(ctx)
	r.afterConnect()
	now = now.Add(2 * time.Minute)
	if c, _ := r.beforeConnect(ctx); c.Password != "p2" {
		t.Errorf("password after maxAge = %q, want p2", c.Password)
	}
}

func TestRotatingCredentialsFetchError(t *testing.T) {
	src := &fakeSource{err: errors.New("boom")}
	r := newRotatingCredentials(src, time.Hour)
	if _, err := r.beforeConnect(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}
