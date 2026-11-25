package render

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTemplateSurvivesStoring(t *testing.T) {
	tpl, err := Compile(doc(t, `{"type":"text","text":"Hello {{first_name|friend}} <{{email}}> of {{company|your team}}"}`, `{"subject":"Hi {{first_name|there}}"}`),
		Account{PostalAddress: "12 Main St", FieldKeys: []string{"company"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(tpl)
	if err != nil {
		t.Fatal(err)
	}
	var back Template
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Recipient{
		{Email: "sam@example.com", FirstName: "Sam", UnsubscribeURL: "https://app.example/u/x", Fields: map[string]string{"company": "Acme"}},
		{Email: "a&b@example.com"},
	} {
		want, got := tpl.Personalize(r), back.Personalize(r)
		if want.Subject != got.Subject || want.HTML != got.HTML || want.Text != got.Text {
			t.Errorf("%+v: stored copy differs", r)
		}
	}
	if !strings.Contains(back.Personalize(Recipient{Fields: map[string]string{"company": "Acme & Co"}}).HTML, "of Acme &amp; Co") {
		t.Error("the stored copy doesn't fill in a contact field's tag")
	}
	// A contact field's tag is kept by its key (MG-133); anything that
	// can't be a key or a built-in tag isn't.
	if err := json.Unmarshal([]byte(`{"subject":[{"tag":"Pass word"}],"html":[],"text":[]}`), &back); err == nil {
		t.Error("unknown tag loaded")
	}
}
