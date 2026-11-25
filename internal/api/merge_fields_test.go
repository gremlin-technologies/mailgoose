package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFieldMergeTags: contact fields are merge tags (MG-133) in the
// preview and the check, and one an active campaign's email uses can't be
// deleted.
func TestFieldMergeTags(t *testing.T) {
	h := fullKey(authServer)(t).Handler()
	do := func(req func(*testing.T) *http.Request) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(req)(t))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec, body
	}
	do(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"Company"}`))
	_, plan := do(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"Plan"}`))
	_, spare := do(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"Spare"}`))
	_, pat := do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com","first_name":"Pat","fields":{"company":"Acme & Co","plan":"Pro"}}`))
	addedEmailFrom(`{"name":"Fields","document":{"settings":{"subject":"News for {{company|your team}}"},"blocks":[
		{"type":"heading","text":"For {{company|your team}}"},
		{"type":"text","text":"Hi {{first_name|there}}, your plan is {{plan}}."},
		{"type":"footer","text":""}]}}`)(t, h)

	// The sample person has no field values, so the fallback shows.
	rec, _ := do(emailPath(http.MethodGet, "/preview", ""))
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, ">For your team</h1>") || !strings.Contains(body, "Hi Alex, your plan is .") {
		t.Errorf("sample preview: %d %.2000s", rec.Code, body)
	}
	// A contact's own values fill it in.
	rec, _ = do(emailPath(http.MethodGet, "/preview?contact_id="+pat["id"].(string), ""))
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, ">For Acme &amp; Co</h1>") || !strings.Contains(body, "Hi Pat, your plan is Pro.") {
		t.Errorf("contact preview: %d %.2000s", rec.Code, body)
	}
	if rec, b := do(emailPath(http.MethodGet, "/preview?contact_id=00000000-0000-4000-8000-000000000000", "")); rec.Code != http.StatusNotFound ||
		b["error"].(map[string]any)["code"] != "not_found" {
		t.Errorf("no such contact: %d %v", rec.Code, b)
	}
	if rec, _ := do(emailPath(http.MethodGet, "/preview?contact_id=pat", "")); rec.Code != http.StatusBadRequest {
		t.Errorf("bad contact id: %d", rec.Code)
	}

	// The check warns about the field tag with no fallback, and nothing
	// else about tags.
	_, res := do(emailPath(http.MethodPost, "/check", ""))
	var tagWarnings []string
	for _, w := range res["warnings"].([]any) {
		if w := w.(map[string]any); w["code"] == "merge_tag_no_fallback" {
			tagWarnings = append(tagWarnings, w["path"].(string))
		}
	}
	if len(tagWarnings) != 1 || tagWarnings[0] != "blocks[1].text" {
		t.Errorf("check warnings: %v", res["warnings"])
	}
	for _, e := range res["errors"].([]any) {
		if e.(map[string]any)["code"] == "merge_tag" {
			t.Errorf("check error: %v", e)
		}
	}

	// A draft campaign's email uses plan, so it can't be deleted; the
	// spare field can.
	v, _ := addedEmails.Load(t)
	if rec, b := do(jsonRequest(http.MethodPost, "/v1/campaigns", `{"name":"Spring","email_id":"`+v.(string)+`","recipients":{"all":true}}`)); rec.Code != http.StatusCreated {
		t.Fatalf("campaign: %d %v", rec.Code, b)
	}
	rec, b := do(request(http.MethodDelete, "/v1/contact-fields/"+plan["id"].(string)))
	e, _ := b["error"].(map[string]any)
	if rec.Code != http.StatusConflict || e["code"] != "field_in_use" ||
		e["message"] != `The email "Fields", in a draft or scheduled campaign, uses {{plan}}, so the field can't be deleted.` {
		t.Fatalf("delete a field in use: %d %v", rec.Code, b)
	}
	details := e["details"].(map[string]any)
	if emails := details["emails"].([]any); details["key"] != "plan" || len(emails) != 1 ||
		emails[0].(map[string]any)["id"] != v.(string) || emails[0].(map[string]any)["name"] != "Fields" {
		t.Errorf("details: %v", details)
	}
	if rec, b := do(request(http.MethodDelete, "/v1/contact-fields/"+spare["id"].(string))); rec.Code != http.StatusOK {
		t.Errorf("delete a field no email uses: %d %v", rec.Code, b)
	}
}
