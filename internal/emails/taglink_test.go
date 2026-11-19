package emails

import (
	"errors"
	"strings"
	"testing"
)

func TestFixedLinkHost(t *testing.T) {
	cases := []struct {
		href string
		want bool
	}{
		{"https://shop.example/menu", true},
		{"{{unsubscribe_url}}", true},
		{" {{ unsubscribe_url }} ", true},
		{"https://shop.example/?name={{first_name}}", true},
		{"https://shop.example/welcome/{{last_name|friend}}", true},
		{"https://shop.example#{{first_name}}", true},
		{"HTTP://shop.example/{{email}}", true},
		{"mailto:{{email}}", true},
		{"{{first_name}}", false},
		{"{{first_name|https://shop.example}}", false},
		{"{{email}}", false},
		{"{{unsubscribe}}", false},
		{"https://shop.example{{last_name}}", false},
		{"https://shop.example.{{last_name}}/", false},
		{"https://shop.example@{{email}}/", false},
		{"https://{{first_name}}/", false},
		{"//{{first_name}}/", false},
		{"/menu/{{first_name}}", false},
		{"javascript:{{first_name}}", false},
	}
	for _, c := range cases {
		if got := FixedLinkHost(c.href); got != c.want {
			t.Errorf("FixedLinkHost(%q) = %v, want %v", c.href, got, c.want)
		}
	}
}

func TestBlockLinksRefuseTagDestinations(t *testing.T) {
	for _, block := range []string{
		`{"type":"button","text":"Shop","href":"{{first_name|https://shop.example}}"}`,
		`{"type":"button","text":"Shop","href":"{{first_name}}"}`,
		`{"type":"image","src":"https://cdn.example/a.png","alt":"A","href":"{{email}}"}`,
		`{"type":"button","text":"Shop","href":"https://shop.example{{last_name}}"}`,
	} {
		_, err := ParseDocument(KindBlocks, []byte(`{"blocks":[`+block+`]}`))
		var de *DocError
		if !errors.As(err, &de) || !strings.Contains(de.Message, "only {{unsubscribe_url}} may be a whole link") {
			t.Errorf("%s: got %v, want the merge tag link refusal", block, err)
		}
	}
	for _, href := range []string{"{{unsubscribe_url}}", "https://shop.example/?name={{first_name}}", "mailto:{{email}}"} {
		if _, err := ParseDocument(KindBlocks, []byte(`{"blocks":[{"type":"button","text":"Go","href":"`+href+`"}]}`)); err != nil {
			t.Errorf("%s: %v", href, err)
		}
	}
}
