package render

import (
	"encoding/json"
	"testing"
)

func TestTemplateSurvivesStoring(t *testing.T) {
	tpl, err := Compile(doc(t, `{"type":"text","text":"Hello {{first_name|friend}} <{{email}}>"}`, `{"subject":"Hi {{first_name|there}}"}`), Account{PostalAddress: "12 Main St"})
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
		{Email: "sam@example.com", FirstName: "Sam", UnsubscribeURL: "https://app.example/u/x"},
		{Email: "a&b@example.com"},
	} {
		want, got := tpl.Personalize(r), back.Personalize(r)
		if want.Subject != got.Subject || want.HTML != got.HTML || want.Text != got.Text {
			t.Errorf("%+v: stored copy differs", r)
		}
	}
	if err := json.Unmarshal([]byte(`{"subject":[{"tag":"password"}],"html":[],"text":[]}`), &back); err == nil {
		t.Error("unknown tag loaded")
	}
}
