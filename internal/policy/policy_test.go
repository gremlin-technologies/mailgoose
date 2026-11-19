package policy

import (
	"bytes"
	"os"
	"regexp"
	"testing"
)

func TestHistory(t *testing.T) {
	date := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	prev := ""
	for i, v := range history {
		if !date.MatchString(v.ID) || !date.MatchString(v.Effective) {
			t.Errorf("version %q: id and effective date must be YYYY-MM-DD", v.ID)
		}
		_ = v.EffectiveDate()
		if v.ID <= prev {
			t.Errorf("version %q comes after %q; versions must increase", v.ID, prev)
		}
		if len(v.Changes) == 0 {
			t.Errorf("version %q says nothing about what changed", v.ID)
		}
		if i == 0 && v.ID != Initial {
			t.Errorf("the first version is %q, want Initial (%q): migration 00036 backfilled it", v.ID, Initial)
		}
		prev = v.ID
	}
	if Current().ID != history[len(history)-1].ID || History()[0].ID != Current().ID {
		t.Error("Current and History disagree")
	}
	if !Known(Initial) || Known("2000-01-01") {
		t.Error("Known")
	}
}

// TestWebCopyUpToDate fails when web/src/policyVersions.gen.ts wasn't
// regenerated after a change here.
func TestWebCopyUpToDate(t *testing.T) {
	got, err := os.ReadFile("../../" + WebFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, TypeScript()) {
		t.Errorf("%s is out of date: run make generate", WebFile)
	}
}
