package render

import (
	"html"
	"strings"

	nethtml "golang.org/x/net/html"
)

// Tracker is what Track needs to make one copy's tracked addresses.
type Tracker interface {
	// Click returns the tracked address of the n-th link (from 1) to
	// target, or target when it isn't one to track.
	Click(n int, target string) string
	// Pixel is the open pixel's address.
	Pixel() string
}

// Track adds open and click tracking (REP-3) to one recipient's HTML: each
// <a href> to an http or https address becomes a tracked address, and a
// 1x1 image goes just before </body>. Links for which keep returns true
// (the unsubscribe link) are left as they are, and so is everything else in
// the markup. Links are numbered in the order they appear, so the same link
// has the same number in every copy.
func Track(src string, t Tracker, keep func(href string) bool) string {
	z := nethtml.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	b.Grow(len(src) + len(src)/4)
	n := 0
	for {
		tt := z.Next()
		if tt == nethtml.ErrorToken {
			break
		}
		if tt != nethtml.StartTagToken && tt != nethtml.SelfClosingTagToken {
			b.Write(z.Raw())
			continue
		}
		// TagName and TagAttr may change what Raw returned, so keep a copy.
		raw := append([]byte(nil), z.Raw()...)
		name, hasAttr := z.TagName()
		if string(name) != "a" || !hasAttr {
			b.Write(raw)
			continue
		}
		tok := tagToken(z, tt, name)
		i := hrefIndex(tok)
		if i < 0 {
			b.Write(raw)
			continue
		}
		href := strings.TrimSpace(tok.Attr[i].Val)
		n++
		if keep != nil && keep(href) {
			b.Write(raw)
			continue
		}
		tracked := t.Click(n, href)
		if tracked == href {
			b.Write(raw)
			continue
		}
		tok.Attr[i].Val = tracked
		b.WriteString(tok.String())
	}
	return addPixel(b.String(), t.Pixel())
}

// tagToken reads the current tag's attributes.
func tagToken(z *nethtml.Tokenizer, tt nethtml.TokenType, name []byte) nethtml.Token {
	tok := nethtml.Token{Type: tt, Data: string(name)}
	for {
		key, val, more := z.TagAttr()
		tok.Attr = append(tok.Attr, nethtml.Attribute{Key: string(key), Val: string(val)})
		if !more {
			return tok
		}
	}
}

func hrefIndex(tok nethtml.Token) int {
	for i, a := range tok.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, "href") {
			return i
		}
	}
	return -1
}

// addPixel puts the open pixel just before the last </body>, or at the end.
func addPixel(src, pixel string) string {
	img := `<img src="` + html.EscapeString(pixel) + `" width="1" height="1" alt="" border="0" ` +
		`style="display:block;width:1px;height:1px;border:0;margin:0;padding:0;">`
	locs := bodyClose.FindAllStringIndex(src, -1)
	if len(locs) == 0 {
		return src + img
	}
	at := locs[len(locs)-1][0]
	return src[:at] + img + src[at:]
}
