package cli

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFormsAgainstAPI(t *testing.T) {
	h := liveAPI(t)
	if code := h.run("tags", "create", "Newsletter"); code != ExitOK {
		t.Fatalf("tag: exit %d %s", code, h.errOut)
	}

	if code := h.run("form", "create", "--first-name", "optional"); code != ExitUsage || !strings.Contains(h.errOut.String(), "--name") {
		t.Errorf("no name: exit %d %s", code, h.errOut)
	}
	if code := h.run("form", "create", "--name", "Footer", "--first-name", "optional", "--tag", "Newsletter", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	f := h.jsonOut(t)
	id := f["id"].(string)
	fields := f["fields"].([]any)
	if len(fields) != 2 || fields[1].(map[string]any)["name"] != "first_name" || fields[1].(map[string]any)["required"] != false ||
		f["double_opt_in"] != true || len(f["tags"].([]any)) != 1 {
		t.Errorf("created %v", f)
	}

	if code := h.run("form", "get", id); code != ExitOK {
		t.Fatalf("get: exit %d %s", code, h.errOut)
	}
	for _, want := range []string{"Name:", "Footer", `email "Email" (required), first_name "First name" (optional)`, "Tags:", "Newsletter",
		`Button:           "Subscribe"`, "Double opt-in:    on: people confirm by email first", "Signups:          0",
		"Confirmed:        0", "This form isn't taking signups. 1 thing to fix:", "no From address on a verified sending domain",
		"mailgoose domain add"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("get lacks %q:\n%s", want, h.out)
		}
	}

	// Update keeps what it isn't told to change; a field already there
	// keeps its place.
	if code := h.run("form", "update", id, "--last-name", "required", "--first-name", "required", "--no-tags",
		"--double-opt-in=false", "--redirect-url", "https://bakery.example/thanks", "--json"); code != ExitOK {
		t.Fatalf("update: exit %d %s", code, h.errOut)
	}
	u := h.jsonOut(t)
	var names []string
	for _, fl := range u["fields"].([]any) {
		m := fl.(map[string]any)
		names = append(names, m["name"].(string))
		if m["required"] != true {
			t.Errorf("field %v", m)
		}
	}
	if strings.Join(names, ",") != "email,first_name,last_name" || u["name"] != "Footer" || u["double_opt_in"] != false ||
		len(u["tags"].([]any)) != 0 || u["redirect_url"] != "https://bakery.example/thanks" {
		t.Errorf("updated %v", u)
	}
	if code := h.run("form", "update", id, "--first-name", "off", "--redirect-url", ""); code != ExitOK ||
		!strings.Contains(h.out.String(), `Updated form "Footer"`) {
		t.Fatalf("update 2: exit %d %s %s", code, h.out, h.errOut)
	}

	// get --json round-trips through --file.
	if code := h.run("form", "get", id, "--json"); code != ExitOK {
		t.Fatal(h.errOut)
	}
	h.app.Stdin = strings.NewReader(strings.Replace(h.out.String(), `"Subscribe"`, `"Join us"`, 1))
	if code := h.run("form", "update", id, "--file", "-", "--json"); code != ExitOK || h.jsonOut(t)["button_text"] != "Join us" {
		t.Fatalf("update --file: exit %d %s %s", code, h.out, h.errOut)
	}
	h.app.Stdin = strings.NewReader(`{"name":"From file","fields":[{"name":"email","label":"Your email"}]}`)
	if code := h.run("form", "create", "--file", "-", "--last-name", "optional", "--json"); code != ExitOK {
		t.Fatalf("create --file: exit %d %s", code, h.errOut)
	}
	if c := h.jsonOut(t); c["name"] != "From file" || len(c["fields"].([]any)) != 2 ||
		c["fields"].([]any)[0].(map[string]any)["label"] != "Your email" {
		t.Errorf("create --file %v", c)
	}

	// The API's refusal names the part.
	if code := h.run("form", "update", id, "--redirect-url", "http://bakery.example"); code == ExitOK || !strings.Contains(h.errOut.String(), "https://") {
		t.Errorf("bad redirect: exit %d %s", code, h.errOut)
	}
	for _, args := range [][]string{
		{"form", "update", id},
		{"form", "update", id, "--first-name", "sometimes"},
		{"form", "update", id, "--tag", "Newsletter", "--no-tags"},
		{"form", "get", "footer"},
		{"form", "delete", id},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d %s", args, code, h.errOut)
		}
	}
	if code := h.run("form", "update", id, "--tag", "nope"); code != ExitNotFound {
		t.Errorf("unknown tag: exit %d %s", code, h.errOut)
	}

	if code := h.run("form", "list"); code != ExitOK || !strings.Contains(h.out.String(), "From file") || !strings.Contains(h.out.String(), "email,last_name") {
		t.Errorf("list: exit %d %s", code, h.out)
	}

	if code := h.run("form", "embed", id); code != ExitOK {
		t.Fatalf("embed: exit %d %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{"<!-- mailgoose signup form -->", `<form class="mg-form"`, "/f/" + id, `name="_mg_ts"`, "<script>"} {
		if !strings.Contains(out, want) {
			t.Errorf("embed lacks %q:\n%s", want, out)
		}
	}
	if code := h.run("form", "embed", id, "--no-script", "--json"); code != ExitOK {
		t.Fatal(h.errOut)
	}
	if e := h.jsonOut(t); e["script"] != "" || !strings.Contains(e["html"].(string), "<form") {
		t.Errorf("--no-script %v", e)
	}

	if code := h.run("form", "delete", id, "--yes"); code != ExitOK || !strings.Contains(h.out.String(), "Deleted form "+id) {
		t.Errorf("delete: exit %d %s", code, h.errOut)
	}
	if code := h.run("form", "get", id); code != ExitNotFound {
		t.Errorf("get deleted: exit %d", code)
	}
}

// A draft form from a Mailchimp import (MG-76) says so, and --live saves it
// as it is.
func TestFormLiveSavesADraft(t *testing.T) {
	h := liveAPI(t)
	if code := h.run("form", "create", "--name", "Bakery news", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)
	v, _ := liveDBs.Load(t)
	if _, err := v.(*pgxpool.Pool).Exec(t.Context(), `UPDATE forms SET draft = true, mailchimp_audience = 'Bakery news'`); err != nil {
		t.Fatal(err)
	}
	if code := h.run("form", "get", id); code != ExitOK {
		t.Fatalf("get: exit %d %s", code, h.errOut)
	}
	for _, want := range []string{"Status:", "draft from your Mailchimp import", "mailgoose form update " + id + " --live",
		"made this form as a draft"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("get lacks %q:\n%s", want, h.out)
		}
	}
	if code := h.run("form", "list"); code != ExitOK || !strings.Contains(h.out.String(), "Bakery news (draft)") {
		t.Errorf("list: exit %d %s", code, h.out)
	}
	if code := h.run("form", "update", id, "--live", "--json"); code != ExitOK {
		t.Fatalf("live: exit %d %s", code, h.errOut)
	}
	if f := h.jsonOut(t); f["draft"] != false || f["name"] != "Bakery news" {
		t.Errorf("live = %v", f)
	}
}
