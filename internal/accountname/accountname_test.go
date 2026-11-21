package accountname

import (
	"strings"
	"testing"
)

func TestCheckRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, want string
	}{
		// An @, also fullwidth.
		{"pat@example.com", "can't contain @"},
		{"Pat ＠ Bakery", "can't contain @"},
		// Shaped like a domain, also with fullwidth or ideographic dots.
		{"paypal.com", "look like a domain"},
		{"Visit secure.my-bank.co.uk today", "look like a domain"},
		{"Bakery．shop", "look like a domain"},
		{"bakery。shop", "look like a domain"},
		{"example.xn--p1ai", "look like a domain"},
		// Brands, anywhere in the name and in lookalike spellings.
		{"PayPal Support", `"PayPal"`},
		{"paypal", `"PayPal"`},
		{"PAYPALSUPPORT", `"PayPal"`},
		{"Pay-Pal", `"PayPal"`},
		{"P a y P a l", `"PayPal"`},
		{"P.a.y.P.a.l Team", `"PayPal"`},
		{"PayPa1", `"PayPal"`},
		{"PAYPAI", `"PayPal"`},
		{"Pаypal", `"PayPal"`}, // Cyrillic а
		{"ΡayΡal", `"PayPal"`}, // Greek Ρ
		{"Ｐａｙｐａｌ", `"PayPal"`},
		{"Pay\u200bPal", `"PayPal"`}, // zero-width space
		{"Pâypál", `"PayPal"`},
		{"Bank of America Alerts", `"Bank of America"`},
		{"Wells-Fargo", `"Wells Fargo"`},
		{"Mlcrosoft", `"Microsoft"`},
		{"Netfl1x", `"Netflix"`},
		{"Arnazon Store", `"Amazon"`}, // rn for m
		{"mailgoose team", `"mailgoose"`},
		{"Amazon", `"Amazon"`},
		{"Amazon's Deals", `"Amazon"`},
		{"eBay Seller Center", `"eBay"`},
		{"Apple", `"Apple"`},
		{"Apple Support", `"Apple Support"`},
		{"The Apple Store", `"Apple"`},
		{"VISA", `"Visa"`},
		{"Chase Customer Service", `"Chase"`},
		// Payment terms.
		{"Billing Department", `"Billing"`},
		{"Pat's Bakery Billing", `"Billing"`},
		{"Bill1ng", `"Billing"`},
		{"Refunds", `"Refunds"`},
		{"Account Security Team", `"Account Security"`},
		{"Wire-Transfer Desk", `"Wire Transfer"`},
	} {
		got := Check(tc.name)
		if got == nil {
			t.Errorf("Check(%q) = allowed; want refused with %s", tc.name, tc.want)
			continue
		}
		if !strings.Contains(got.Message, tc.want) || !strings.Contains(got.Message, "shared domain") {
			t.Errorf("Check(%q) = %q; want it to say %s and why", tc.name, got.Message, tc.want)
		}
	}
}

func TestCheckAllows(t *testing.T) {
	for _, name := range []string{
		"Pat's Bakery",
		"Café Olé",
		"Big Apple Bagels",
		"Pineapple Express Juice",
		"Amazonia Coffee",
		"Riverbank Farm",
		"Food Bank of Atlanta",
		"Chase's Coffee",
		"Visage Salon",
		"Square One Studio",
		"Gazelle Running Club",
		"Purchase Point",
		"A.B.C. Plumbing",
		"Acme Ltd.",
		"Version 2.0 Labs",
		"Dr. Who Fan Club",
		"Ed's Bikes, est. 1990",
		"Micrsoft",
		"Bill's Diner",
		"Paid in Full Bakery",
		// A brand doesn't run across a word's edge (PR 218 review).
		"Coastal Tunes",
		"Small Cloud Studio",
		"Purchase Banking Co",
		"Apple Ideas",
	} {
		if r := Check(name); r != nil {
			t.Errorf("Check(%q) = %q; want allowed", name, r.Message)
		}
	}
}

func TestTokens(t *testing.T) {
	got := strings.Join(Tokens("  Pât's  ＣＡＦＥ—Bill1ng, ΡayΡal "), " ")
	if want := "pat s cafe bllllng paypal"; got != want {
		t.Errorf("Tokens = %q; want %q", got, want)
	}
}
