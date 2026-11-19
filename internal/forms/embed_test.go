package forms

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func TestEmbed(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	f := &Form{
		ID:         "3f1c9a2e-6b7d-4c1e-9f0a-1b2c3d4e5f60",
		Fields:     []Field{{Name: "first_name", Label: `Name "please" <b>`}, {Name: "email", Label: "Email", Required: true}},
		ButtonText: "Join & save",
		SigningKey: key,
	}
	now := time.Unix(1_800_000_000, 0)
	e := MakeEmbed(f, "https://app.mailgoose.example", now)
	if e.ActionURL != "https://app.mailgoose.example/f/3f1c9a2e-6b7d-4c1e-9f0a-1b2c3d4e5f60" {
		t.Errorf("action %s", e.ActionURL)
	}

	// Parse the HTML and look at what a browser would post.
	doc, err := html.Parse(strings.NewReader(e.HTML))
	if err != nil {
		t.Fatal(err)
	}
	var form *html.Node
	inputs := map[string]map[string]string{}
	var labels, buttons []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			attrs := map[string]string{}
			for _, a := range n.Attr {
				attrs[a.Key] = a.Val
			}
			switch n.Data {
			case "form":
				form = n
				if attrs["action"] != e.ActionURL || attrs["method"] != "post" {
					t.Errorf("form attrs %v", attrs)
				}
			case "input":
				inputs[attrs["name"]] = attrs
			case "label":
				if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
					labels = append(labels, strings.TrimSpace(n.FirstChild.Data))
				}
			case "button":
				buttons = append(buttons, n.FirstChild.Data)
			case "script":
				t.Error("the form's HTML has a script; it must work without one")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if form == nil {
		t.Fatal("no form")
	}
	if inputs["email"]["type"] != "email" || inputs["email"]["required"] != "" || inputs["email"]["autocomplete"] != "email" {
		t.Errorf("email input %v", inputs["email"])
	}
	if _, req := inputs["first_name"]["required"]; req || inputs["first_name"]["autocomplete"] != "given-name" {
		t.Errorf("first name input %v", inputs["first_name"])
	}
	if _, ok := inputs["last_name"]; ok {
		t.Error("the form doesn't ask for a last name")
	}
	if hp := inputs[HoneypotField]; hp == nil || hp["tabindex"] != "-1" || hp["autocomplete"] != "off" {
		t.Errorf("honeypot %v", hp)
	}
	if got, err := CheckStamp(key, f.ID, inputs[StampField]["value"]); err != nil || !got.Equal(now) || inputs[StampField]["type"] != "hidden" {
		t.Errorf("stamp %v: %v %v", inputs[StampField], got, err)
	}
	if strings.Join(labels, "|") != `Name "please" <b>|Email|Leave this empty` || strings.Join(buttons, "|") != "Join & save" {
		t.Errorf("labels %q buttons %q", labels, buttons)
	}
	if strings.Contains(e.HTML, "<b>") || !strings.Contains(e.HTML, "&lt;b&gt;") {
		t.Error("labels must be escaped")
	}

	// The optional script names this form and sends the elapsed time.
	for _, want := range []string{"<script>", `form[data-mg-form="` + f.ID + `"]`, "'" + ElapsedField + "'", "Accept: 'application/json'", "</script>"} {
		if !strings.Contains(e.Script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
}
