// Package accountname checks the names accounts may take (MG-98).
//
// On mailgoose's shared sending domain (MG-85) every email goes out under
// its account's name, so the name is what a recipient reads as the sender.
// Sign-up and rename refuse a name that could pass for someone else there:
// one with an @ in it, one shaped like a domain (paypal.com), or one that
// carries a well-known brand or a payment term (PayPal Support, Billing
// Department).
//
// Names are compared after normalising them: compatibility forms folded
// (fullwidth letters, ligatures), accents and invisible characters dropped,
// case folded, and common lookalikes mapped to one letter (Cyrillic and
// Greek letters that look Latin, 0 for o, 1 and i for l, rn for m, and so
// on). Spaces and punctuation are ignored, so "Pay-Pal", "P a y P a l" and
// "PAYPA1" all read as PayPal. The lists are a reversible default
// (decisions, 2026-11-21): short on purpose, and easy to extend.
package accountname

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Refusal is Check's answer for a name an account may not take. Message
// says why, for the person choosing it.
type Refusal struct {
	// Term is the brand or payment term the name carries, in its usual
	// spelling; empty when the name was refused for an @ or a domain.
	Term    string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// why is the reason every refusal shares.
const why = "Emails sent from mailgoose's shared domain go out under your account's name"

// Check returns nil when name may be an account's name, and a *Refusal
// saying why when it may not. It doesn't check length or blank names; the
// callers do.
func Check(name string) *Refusal {
	compat := compatible(name)
	if strings.ContainsRune(compat, '@') {
		return &Refusal{Message: "An account name can't contain @. " + why + ", so it can't look like an email address."}
	}
	if d := domainShape.FindStringSubmatch(compat); d != nil {
		return &Refusal{Message: fmt.Sprintf("An account name can't look like a domain, as %q does. %s, so it can't look like a web or email address.", d[1], why)}
	}
	tokens := Tokens(name)
	joined := strings.Join(tokens, "")
	for _, t := range terms {
		if t.matches(tokens, joined) {
			kind := "a well-known brand"
			if t.kind == payment {
				kind = "a payment or account-security term"
			}
			return &Refusal{Term: t.display, Message: fmt.Sprintf("An account name can't include %q, %s. %s, so it can't pass for a company or a bank's or shop's own messages. Choose another name.",
				t.display, kind, why)}
		}
	}
	return nil
}

// compatible is name with compatibility forms folded (NFKC: a fullwidth @
// is @), invisible format characters (zero-width spaces, joiners, soft
// hyphens) dropped, the ideographic full stop read as a dot, and case
// folded.
func compatible(name string) string {
	var b strings.Builder
	for _, r := range norm.NFKC.String(name) {
		switch {
		case unicode.Is(unicode.Cf, r):
			continue
		case r == '。':
			r = '.'
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// label is one label of a domain name; domainShape is two or more of them
// joined by dots, the last a top-level domain of letters (or punycode),
// standing on its own: "paypal.com", "secure.my-bank.co.uk".
const label = `[\p{L}\p{N}](?:[\p{L}\p{N}-]{0,61}[\p{L}\p{N}])?`

var domainShape = regexp.MustCompile(`(?:^|[^\p{L}\p{N}-])(` + label + `(?:\.` + label + `)*\.(?:\p{L}{2,63}|xn--[a-z0-9-]{1,59}))(?:$|[^\p{L}\p{N}-])`)

// lookalikes maps characters that pass for Latin letters to them. Upper
// and lower case are both listed, since some only look alike in one case.
var lookalikes = map[rune]string{
	// Digits and symbols used for letters.
	'0': "o", '1': "l", '3': "e", '4': "a", '5': "s", '7': "t", '8': "b",
	'$': "s", '|': "l", 'ß': "ss", 'ı': "l", 'ł': "l", 'ø': "o", 'đ': "d", 'ħ': "h",
	// Cyrillic.
	'А': "a", 'а': "a", 'В': "b", 'Ь': "b", 'ь': "b", 'С': "c", 'с': "c", 'ԁ': "d",
	'Е': "e", 'е': "e", 'Ё': "e", 'ё': "e", 'Н': "h", 'һ': "h", 'І': "l", 'і': "l",
	'Ї': "l", 'ї': "l", 'Ј': "j", 'ј': "j", 'К': "k", 'к': "k", 'М': "m", 'О': "o",
	'о': "o", 'Р': "p", 'р': "p", 'Ԛ': "q", 'ԛ': "q", 'Ѕ': "s", 'ѕ': "s", 'Т': "t",
	'Х': "x", 'х': "x", 'У': "y", 'у': "y", 'Ү': "y", 'ү': "y", 'Ԝ': "w", 'ԝ': "w",
	'ѡ': "w", 'Ӏ': "l", 'ӏ': "l",
	// Greek.
	'Α': "a", 'α': "a", 'Β': "b", 'β': "b", 'Ε': "e", 'ε': "e", 'Ζ': "z", 'Η': "h",
	'Ι': "l", 'ι': "l", 'Κ': "k", 'κ': "k", 'Μ': "m", 'Ν': "n", 'ν': "v", 'Ο': "o",
	'ο': "o", 'Ρ': "p", 'ρ': "p", 'Τ': "t", 'τ': "t", 'Υ': "y", 'υ': "u", 'Χ': "x",
	'χ': "x",
}

// Tokens is name normalised for comparison, as its words: compatibility
// forms folded, accents and invisible characters dropped, lookalikes
// mapped, case folded, then i read as l, rn as m and vv as w (each pair
// looks like the other). Anything that isn't a letter or digit separates
// words, so apostrophes and hyphens do too.
func Tokens(name string) []string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(norm.NFKC.String(name)) {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if s, ok := lookalikes[r]; ok {
			b.WriteString(s)
			continue
		}
		r = unicode.ToLower(r)
		if s, ok := lookalikes[r]; ok {
			b.WriteString(s)
			continue
		}
		if r == 'i' {
			r = 'l'
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	words := strings.Fields(b.String())
	for i, w := range words {
		w = strings.ReplaceAll(w, "rn", "m")
		words[i] = strings.ReplaceAll(w, "vv", "w")
	}
	return words
}

type kind int

const (
	// anywhere is a brand distinctive enough to refuse wherever it shows,
	// even inside a word: "PayPalSupport".
	anywhere kind = iota
	// word is a brand refused as a word or words of the name, so
	// "Amazon Store" is refused and "Amazonia Coffee" isn't.
	word
	// alone is a brand that's also an everyday word: refused only when the
	// name is the brand with at most generic words around it ("Apple",
	// "Apple Support"), so "Big Apple Bagels" isn't.
	alone
	// payment is a payment or account-security term, refused as a word or
	// words of the name.
	payment
)

type term struct {
	display string
	kind    kind
	skel    string
}

// brandTerms and paymentTerms are the names refused (reversible defaults).
var brandTerms = map[kind][]string{
	anywhere: {
		"PayPal", "Mastercard", "American Express", "Wells Fargo", "Bank of America", "Citibank",
		"JPMorgan", "Chase Bank", "Capital One", "Western Union", "MoneyGram", "Cash App", "Venmo",
		"Coinbase", "Binance", "Apple Pay", "Apple ID", "Apple Support", "iCloud", "iTunes",
		"Google Pay", "Microsoft", "Facebook", "Instagram", "WhatsApp", "LinkedIn", "Netflix",
		"DocuSign", "QuickBooks", "Mailchimp", "mailgoose", "Mailer Daemon", "Postmaster",
		"Amazon Pay", "Amazon Prime", "Amazon Support",
	},
	word: {
		"Amazon", "Google", "Gmail", "eBay", "Zelle", "Stripe", "Shopify", "Klarna", "Afterpay",
		"Revolut", "Amex", "Intuit", "FedEx", "DHL", "USPS", "IRS", "HMRC", "Yahoo",
	},
	alone: {"Apple", "Chase", "Visa", "Square", "Meta", "Discover", "Citi", "Target", "Wise", "Uber"},
}

var paymentTerms = []string{
	"Billing", "Invoice", "Invoices", "Payment", "Payments", "Refund", "Refunds", "Chargeback",
	"Fraud", "Wire Transfer", "Bank Transfer", "Direct Deposit", "Tax Refund", "Account Security",
	"Security Alert", "Account Verification", "Verification", "Password",
}

// generic are words that, around an alone brand, still read as the brand
// itself: "Apple Support", "The Chase Team".
var generic = map[string]bool{}

func init() {
	for _, w := range []string{"the", "official", "support", "help", "helpdesk", "team", "service", "services",
		"customer", "customers", "care", "security", "account", "accounts", "billing", "pay", "payments",
		"id", "alert", "alerts", "notice", "notices", "notification", "notifications", "store", "inc", "llc",
		"ltd", "corp", "co", "online", "us", "uk"} {
		generic[skeleton(w)] = true
	}
}

// terms are every refused term, normalised.
var terms = func() []term {
	var out []term
	for _, k := range []kind{anywhere, word, alone} {
		for _, d := range brandTerms[k] {
			out = append(out, term{display: d, kind: k, skel: skeleton(d)})
		}
	}
	for _, d := range paymentTerms {
		out = append(out, term{display: d, kind: payment, skel: skeleton(d)})
	}
	return out
}()

func skeleton(s string) string { return strings.Join(Tokens(s), "") }

func (t term) matches(tokens []string, joined string) bool {
	switch t.kind {
	case anywhere:
		return strings.Contains(joined, t.skel)
	case alone:
		var rest strings.Builder
		for _, w := range tokens {
			if !generic[w] {
				rest.WriteString(w)
			}
		}
		return rest.String() == t.skel
	default:
		return wordRun(tokens, t.skel)
	}
}

// wordRun reports whether some run of consecutive tokens spells skel, so
// "Pay Pal" and "P.a.y.P.a.l" spell paypal as "PayPal" does.
func wordRun(tokens []string, skel string) bool {
	for i := range tokens {
		var run string
		for _, w := range tokens[i:] {
			run += w
			if run == skel {
				return true
			}
			if len(run) >= len(skel) || !strings.HasPrefix(skel, run) {
				break
			}
		}
	}
	return false
}
