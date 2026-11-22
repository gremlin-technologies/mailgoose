package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

// MG-76: confirming a Mailchimp import gives each audience a draft signup
// form.

type bundleFormsJSON struct {
	Status  string `json:"status"`
	Preview *struct {
		Audiences []struct {
			Name        string   `json:"name"`
			Groups      []string `json:"groups"`
			DoubleOptin *bool    `json:"double_optin"`
		} `json:"audiences"`
	} `json:"preview"`
	Forms []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Audience string `json:"audience"`
		New      bool   `json:"new"`
	} `json:"forms"`
}

type formJSONForTest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Draft       bool   `json:"draft"`
	DoubleOptIn bool   `json:"double_opt_in"`
	Fields      []struct {
		Name string `json:"name"`
	} `json:"fields"`
	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
	Problems []struct {
		Code string `json:"code"`
	} `json:"problems"`
}

func uploadBundle(t *testing.T, h http.Handler, req func(*testing.T) *http.Request) (string, bundleFormsJSON) {
	t.Helper()
	rec := do(t, h, bearer(req)(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		ID string `json:"id"`
	}
	decodeInto(t, rec, &out)
	var b bundleFormsJSON
	decodeInto(t, rec, &b)
	return out.ID, b
}

func confirmBundle(t *testing.T, h http.Handler, id, body string) bundleFormsJSON {
	t.Helper()
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/bundles/"+id+"/confirm", body))(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	var b bundleFormsJSON
	decodeInto(t, rec, &b)
	return b
}

func getForm(t *testing.T, h http.Handler, id string) formJSONForTest {
	t.Helper()
	rec := do(t, h, bearer(request(http.MethodGet, "/v1/forms/"+id))(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("get form: %d %s", rec.Code, rec.Body)
	}
	var f formJSONForTest
	decodeInto(t, rec, &f)
	return f
}

func (f formJSONForTest) tagNames() []string {
	var out []string
	for _, t := range f.Tags {
		out = append(out, t.Name)
	}
	slices.Sort(out)
	return out
}

func TestMailchimpImportMakesADraftFormForOneAudience(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	csv := "Email Address,First Name,Last Name\nada@example.com,Ada,Lovelace\n"
	id, up := uploadBundle(t, h, mailchimpUpload("subscribed_members_export_x.csv", func(*testing.T) []byte { return []byte(csv) },
		[2]string{"audience_name", "Bakery news"}))
	if up.Preview == nil || len(up.Preview.Audiences) != 1 || up.Preview.Audiences[0].Groups == nil ||
		len(up.Preview.Audiences[0].Groups) != 0 || up.Preview.Audiences[0].DoubleOptin != nil {
		t.Fatalf("preview = %+v", up.Preview)
	}
	if len(up.Forms) != 0 {
		t.Fatalf("forms before confirming = %+v", up.Forms)
	}
	conf := confirmBundle(t, h, id, confirmBody)
	if len(conf.Forms) != 1 || conf.Forms[0].Name != "Bakery news" || conf.Forms[0].Audience != "Bakery news" || !conf.Forms[0].New {
		t.Fatalf("forms = %+v", conf.Forms)
	}
	f := getForm(t, h, conf.Forms[0].ID)
	if !f.Draft || !f.DoubleOptIn || len(f.Fields) != 3 || f.Fields[1].Name != "first_name" || f.Fields[2].Name != "last_name" ||
		len(f.Tags) != 0 || len(f.Problems) == 0 || f.Problems[0].Code != "draft" {
		t.Fatalf("form = %+v", f)
	}
	// Confirming again lists the same form.
	again := confirmBundle(t, h, id, confirmBody)
	if len(again.Forms) != 1 || again.Forms[0].ID != conf.Forms[0].ID {
		t.Errorf("confirm again = %+v", again.Forms)
	}
	// Saving it makes it live.
	rec := do(t, h, bearer(jsonRequest(http.MethodPatch, "/v1/forms/"+f.ID, `{}`))(t))
	var saved formJSONForTest
	decodeInto(t, rec, &saved)
	if rec.Code != http.StatusOK || saved.Draft {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
}

func TestMailchimpImportMakesDraftFormsForSeveralAudiences(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	upload := mailchimpUpload("mailchimp-export.zip", mailchimpZip, [2]string{"group_columns", "Interests"})
	id, up := uploadBundle(t, h, upload)
	if up.Preview == nil || len(up.Preview.Audiences) != 2 {
		t.Fatalf("preview = %+v", up.Preview)
	}
	groups := map[string][]string{}
	for _, a := range up.Preview.Audiences {
		groups[a.Name] = a.Groups
	}
	if !slices.Equal(groups["Newsletter"], []string{"Interests: Recipes", "Interests: Travel"}) || len(groups["Recipe Club"]) != 0 {
		t.Fatalf("groups = %v", groups)
	}

	// A tag that isn't one of the audiences' groups is refused.
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/bundles/"+id+"/confirm",
		`{"permission_confirmed":true,"form_tags":["Interests: Knitting"]}`))(t))
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "unknown_form_tag" {
		t.Fatalf("unknown form tag: %d %s", rec.Code, rec.Body)
	}

	conf := confirmBundle(t, h, id, `{"permission_confirmed":true,"form_tags":["interests: travel"]}`)
	if len(conf.Forms) != 2 {
		t.Fatalf("forms = %+v", conf.Forms)
	}
	byAudience := map[string]string{}
	for _, f := range conf.Forms {
		if !f.New || f.Name != f.Audience {
			t.Errorf("form = %+v", f)
		}
		byAudience[f.Audience] = f.ID
	}
	news := getForm(t, h, byAudience["Newsletter"])
	if !news.Draft || !slices.Equal(news.tagNames(), []string{"Interests: Travel", "audience: Newsletter"}) {
		t.Errorf("newsletter form = %+v", news)
	}
	club := getForm(t, h, byAudience["Recipe Club"])
	if !club.Draft || !slices.Equal(club.tagNames(), []string{"audience: Recipe Club"}) {
		t.Errorf("recipe club form = %+v", club)
	}

	// Importing the same export again makes no second form.
	id2, _ := uploadBundle(t, h, upload)
	again := confirmBundle(t, h, id2, `{"permission_confirmed":true,"idempotency_key":"second"}`)
	if len(again.Forms) != 2 {
		t.Fatalf("forms again = %+v", again.Forms)
	}
	for _, f := range again.Forms {
		if f.New || f.ID != byAudience[f.Audience] {
			t.Errorf("form again = %+v", f)
		}
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/forms"))(t))
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	decodeInto(t, rec, &list)
	if len(list.Data) != 2 {
		t.Errorf("forms = %s", rec.Body)
	}
}
