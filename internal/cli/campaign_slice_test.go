package cli

import (
	"strings"
	"testing"
	"time"
)

// TestSliceDescribe checks only a waiting slice says the rest is still to
// come; a cancelled one says the rest wasn't sent.
func TestSliceDescribe(t *testing.T) {
	at := time.Date(2026, 11, 19, 11, 5, 0, 0, time.UTC)
	for _, c := range []struct {
		s     campaignSlice
		want  string
		waits bool
	}{
		{campaignSlice{State: "sending_slice", Size: 200}, "sending the first 200 recipients", true},
		{campaignSlice{State: "slice_hold", Size: 200, ReleaseAt: &at}, "the rest waits until", true},
		{campaignSlice{State: "held", Size: 200, ReleaseAt: &at}, "the rest is held until", true},
		{campaignSlice{State: "released", Size: 200}, "the rest was released", false},
		{campaignSlice{State: "stopped", Size: 200}, "the rest wasn't sent", false},
		{campaignSlice{State: "cancelled", Size: 200}, "the campaign was cancelled, so the rest wasn't sent", false},
	} {
		got := c.s.describe(time.UTC)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want it to contain %q", c.s.State, got, c.want)
		}
		if waits := strings.Contains(got, "waits") || strings.Contains(got, "held"); waits != c.waits {
			t.Errorf("%s: %q says the rest waits: %v", c.s.State, got, waits)
		}
	}
}
