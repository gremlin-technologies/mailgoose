package emailcheck

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/linkrisk"
)

func safetyDoc(t *testing.T, extra string) Input {
	in := good
	in.Doc = blocksDoc(t, `{"subject":"Spring menu"}`,
		`{"type":"text","text":"See [the menu](https://bakery.example/menu) and [win](http://prize.example/claim)."},`+
			`{"type":"button","text":"Order","href":"https://bakery.example/menu"},`+extra+
			`{"type":"footer","text":""}`)
	return in
}

func TestUnsafeLinkIsAnError(t *testing.T) {
	f := &linkrisk.Fake{}
	f.List("http://prize.example/claim", linkrisk.SocialEngineering, linkrisk.Malware)
	in := safetyDoc(t, "")
	in.Safety = &linkrisk.Checker{Lookup: f}
	res := Check(context.Background(), in, &fakeLinks{})
	if got := codes(res.Errors); got != "unsafe_link@http://prize.example/claim" {
		t.Fatalf("errors %s", got)
	}
	if res.OK() {
		t.Error("an email with an unsafe link is OK")
	}
	if msg := res.Errors[0].Message; msg != "The link http://prize.example/claim is on Google's list of phishing and malware sites, so the email can't be sent with it." {
		t.Errorf("message %q", msg)
	}
	if got := codes(res.Warnings); got != "insecure_link@http://prize.example/claim" {
		t.Errorf("warnings %s", got)
	}
	// Each distinct link once, and never the unsubscribe link.
	calls := f.Calls()
	slices.Sort(calls)
	if strings.Join(calls, " ") != "http://prize.example/claim https://bakery.example/menu" {
		t.Errorf("looked up %v", calls)
	}
}

func TestTrackedLinkLookedUpByTarget(t *testing.T) {
	// One of mailgoose's own tracked links, pasted into an email: the
	// target inside the token is what's looked up.
	body := make([]byte, 16+16+8+2)
	body = append(body, "https://evil.example/x"...)
	body = append(body, make([]byte, 16)...)
	tracked := "https://app.mailgoose.app/t/c/t1" + base64.RawURLEncoding.EncodeToString(body)

	f := &linkrisk.Fake{}
	f.List("https://evil.example/x", linkrisk.Malware)
	in := safetyDoc(t, `{"type":"button","text":"Go","href":"`+tracked+`"},`)
	in.Safety = &linkrisk.Checker{Lookup: f}
	res := Check(context.Background(), in, nil)
	if got := codes(res.Errors); got != "unsafe_link@"+tracked {
		t.Errorf("errors %s", got)
	}
	if !slices.Contains(f.Calls(), "https://evil.example/x") || slices.Contains(f.Calls(), tracked) {
		t.Errorf("looked up %v", f.Calls())
	}
}

func TestLinkSafetyFailsOpen(t *testing.T) {
	for name, c := range map[string]*linkrisk.Checker{
		"no key":        {},
		"Web Risk down": {Lookup: &linkrisk.Fake{Err: errors.New("503")}},
	} {
		t.Run(name, func(t *testing.T) {
			in := safetyDoc(t, "")
			in.Safety = c
			res := Check(context.Background(), in, nil)
			if !res.OK() {
				t.Errorf("errors %s", codes(res.Errors))
			}
			if got := codes(res.Warnings); got != "insecure_link@http://prize.example/claim link_safety_not_checked@" {
				t.Errorf("warnings %s", got)
			}
			for _, w := range res.Warnings {
				if w.Code == LinkSafetyNotChecked && !strings.HasPrefix(w.Message, "Links weren't checked against Google's phishing and malware lists: ") {
					t.Errorf("message %q", w.Message)
				}
			}
		})
	}
}

func TestSafetyHelper(t *testing.T) {
	f := &linkrisk.Fake{}
	f.List("https://bad.example/", linkrisk.UnwantedSoftware)
	src := `<a href="https://bad.example/">x</a> <a href="https://ok.example/">y</a> <a href="mailto:a@b.example">m</a>` +
		`<a href="https://app.example/u/test">unsubscribe</a>`
	errs, warns := Safety(context.Background(), src, &linkrisk.Checker{Lookup: f}, "https://app.example/u/test")
	if codes(errs) != "unsafe_link@https://bad.example/" || len(warns) != 0 {
		t.Errorf("errors %s, warnings %s", codes(errs), codes(warns))
	}
	if calls := f.Calls(); len(calls) != 2 || slices.Contains(calls, "https://app.example/u/test") {
		t.Errorf("looked up %v", calls)
	}
	if errs, warns := Safety(context.Background(), src, nil); errs != nil || warns != nil {
		t.Error("a nil checker checks nothing")
	}
}
