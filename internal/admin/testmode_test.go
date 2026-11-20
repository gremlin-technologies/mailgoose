package admin

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// TestSendingModeSwitches lists accounts in test mode, allows full sending
// and puts one back in test mode, each logged (MG-80).
func TestSendingModeSwitches(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	lee := h.account(t, "Lee's Shop", "lee@shop.example")
	h.now = h.now.Add(time.Minute)
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_mode = 'test' WHERE id = $1`, lee); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO test_addresses (account_id, email, confirmed_at, created_at) VALUES ($1, 'qa1@example.com', now(), now())`,
		`INSERT INTO test_addresses (account_id, email, created_at) VALUES ($1, 'qa2@example.com', now())`,
	} {
		if _, err := h.db.Exec(ctx, q, lee); err != nil {
			t.Fatal(err)
		}
	}

	list, err := h.svc.ListTestMode(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != lee || list[0].SendingMode != "test" ||
		list[0].TestAddressesConfirmed != 1 || list[0].TestAddressesPending != 1 {
		t.Fatalf("in test mode = %+v", list)
	}

	acc, err := h.svc.AllowFullSending(ctx, h.staff, lee, "Helping us test sending.")
	if err != nil {
		t.Fatal(err)
	}
	if acc.SendingMode != "full" {
		t.Errorf("allowed = %+v", acc.AccountSummary)
	}
	if _, err := h.svc.AllowFullSending(ctx, h.staff, lee, "Again."); !errors.Is(err, ErrSendingModeUnchanged) {
		t.Errorf("allow again: %v", err)
	}
	if list, _ := h.svc.ListTestMode(ctx, 10); len(list) != 0 {
		t.Errorf("still in test mode: %+v", list)
	}

	acc, err = h.svc.SetTestMode(ctx, h.staff, h.acc, "Internal testing account.")
	if err != nil {
		t.Fatal(err)
	}
	if acc.SendingMode != "test" {
		t.Errorf("set = %+v", acc.AccountSummary)
	}
	if _, err := h.svc.SetTestMode(ctx, h.staff, h.acc, "Again."); !errors.Is(err, ErrSendingModeUnchanged) {
		t.Errorf("set again: %v", err)
	}

	var got []string
	for _, l := range h.log(t) {
		if l.action == ActionAllowFullSending || l.action == ActionSetTestMode {
			got = append(got, l.action+":"+l.reason)
		}
	}
	if want := []string{"allow_full_sending:Helping us test sending.", "set_test_mode:Internal testing account."}; !slices.Equal(got, want) {
		t.Errorf("logged %v, want %v", got, want)
	}

	if _, err := h.svc.AllowFullSending(ctx, h.staff, h.acc, " "); err == nil {
		t.Error("allowed without a reason")
	}
	if _, err := h.svc.SetTestMode(ctx, h.staff, "00000000-0000-0000-0000-000000000000", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}
