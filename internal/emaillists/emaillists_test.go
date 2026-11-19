package emaillists

import "testing"

func TestDisposable(t *testing.T) {
	for d, want := range map[string]bool{
		"mailinator.com":     true,
		"Mailinator.COM.":    true,
		"x.y.mailinator.com": true,
		"guerrillamail.com":  true,
		"gmail.com":          false,
		"example.com":        false,
		"com":                false,
		"":                   false,
	} {
		if got := Disposable(d); got != want {
			t.Errorf("Disposable(%q) = %v, want %v", d, got, want)
		}
	}
	if n, _ := Sizes(); n < 5000 {
		t.Errorf("only %d disposable domains loaded", n)
	}
}

func TestRole(t *testing.T) {
	for l, want := range map[string]bool{
		"info": true, "Sales": true, "admin+news": true, "no.reply": true, "no-reply": true, "postmaster": true,
		"pat": false, "pat.info": false, "": false,
	} {
		if got := Role(l); got != want {
			t.Errorf("Role(%q) = %v, want %v", l, got, want)
		}
	}
}

func TestAddress(t *testing.T) {
	l, d, ok := Address("pat@example.com")
	if !ok || l != "pat" || d != "example.com" {
		t.Fatalf("Address = %q %q %v", l, d, ok)
	}
	if _, _, ok := Address("nope"); ok {
		t.Fatal("no @ should fail")
	}
}

func TestFreeMail(t *testing.T) {
	for domain, want := range map[string]bool{
		"gmail.com": true, "Outlook.com": true, "proton.me": true,
		"patsbakery.com": false, "mail.gmail.com": false,
	} {
		if got := FreeMail(domain); got != want {
			t.Errorf("FreeMail(%q) = %v", domain, got)
		}
	}
}

// A free-mail provider is never disposable.
func TestListsDontOverlap(t *testing.T) {
	if len(freeMail) < 20 {
		t.Errorf("freemail: %d entries", len(freeMail))
	}
	for d := range freeMail {
		if _, ok := disposable[d]; ok {
			t.Errorf("%s is on both lists", d)
		}
	}
}
