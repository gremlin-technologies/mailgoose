package forms

import (
	"html"
	"strings"
	"time"
)

// Names of the hidden fields the embed code posts besides the form's own.
const (
	// HoneypotField is hidden from people; a bot that fills it in gives
	// itself away.
	HoneypotField = "website"
	// StampField holds the signed time the embed code was made.
	StampField = "_mg_ts"
	// ElapsedField is how long the page was open before the post, in
	// milliseconds, when the optional script sends it.
	ElapsedField = "_mg_ms"
)

// ActionPath is the public endpoint a form posts to, under the app's URL.
// MG-36 takes the posts.
func ActionPath(formID string) string { return "/f/" + formID }

// Embed is a form's embed code.
type Embed struct {
	// ActionURL is where the form posts.
	ActionURL string
	// HTML is the form itself. It works with no script at all: the post
	// goes to ActionURL, which redirects back with the result.
	HTML string
	// Script is optional: pasted after the form, it posts in the page and
	// shows the success message there, or follows the redirect.
	Script string
}

// MakeEmbed builds the embed code for a form that posts to baseURL (the
// app's public URL, without a trailing slash), stamped with now.
func MakeEmbed(f *Form, baseURL string, now time.Time) Embed {
	action := baseURL + ActionPath(f.ID)
	esc := html.EscapeString
	var b strings.Builder
	w := func(parts ...string) {
		for _, p := range parts {
			b.WriteString(p)
		}
	}
	w(`<form class="mg-form" data-mg-form="`, esc(f.ID), `" action="`, esc(action), `" method="post" accept-charset="utf-8">`, "\n")
	for _, fl := range f.Fields {
		typ, auto := "text", "given-name"
		switch fl.Name {
		case FieldEmail:
			typ, auto = "email", "email"
		case FieldLastName:
			auto = "family-name"
		}
		req := ""
		if fl.Required {
			req = " required"
		}
		w(`  <label class="mg-field">`, esc(fl.Label), "\n",
			`    <input type="`, typ, `" name="`, fl.Name, `" autocomplete="`, auto, `"`, req, ">\n",
			"  </label>\n")
	}
	// The honeypot: off screen, skipped by the keyboard and by screen
	// readers, so only a bot fills it in.
	w(`  <div class="mg-hp" aria-hidden="true"><label>Leave this empty<input type="text" name="`, HoneypotField,
		`" tabindex="-1" autocomplete="off"></label></div>`, "\n")
	w(`  <input type="hidden" name="`, StampField, `" value="`, Stamp(f.SigningKey, f.ID, now), `">`, "\n")
	w(`  <button type="submit" class="mg-button">`, esc(f.ButtonText), "</button>\n")
	w(`  <p class="mg-note" role="status" hidden></p>`, "\n")
	w("</form>\n")
	// Basic styling that takes the site's own fonts and colors.
	w(`<style>
.mg-form{display:grid;gap:12px;max-width:420px;font:inherit;color:inherit}
.mg-form .mg-field{display:grid;gap:4px}
.mg-form input{font:inherit;padding:10px 12px;border:1px solid #c8c8d0;border-radius:6px}
.mg-form .mg-button{font:inherit;font-weight:600;padding:10px 16px;border:0;border-radius:6px;background:#5562a3;color:#fff;cursor:pointer}
.mg-form .mg-button[disabled]{opacity:.6;cursor:default}
.mg-form .mg-hp{position:absolute;left:-10000px;width:1px;height:1px;overflow:hidden}
</style>
`)
	script := `<script>
(function () {
  var form = document.querySelector('form[data-mg-form="` + f.ID + `"]');
  if (!form || !window.fetch || !window.URLSearchParams || !window.FormData) return;
  var opened = Date.now();
  form.addEventListener('submit', function (event) {
    event.preventDefault();
    var data = new URLSearchParams(new FormData(form));
    data.set('` + ElapsedField + `', String(Date.now() - opened));
    var button = form.querySelector('.mg-button');
    var note = form.querySelector('.mg-note');
    button.disabled = true;
    fetch(form.action, { method: 'POST', body: data, headers: { Accept: 'application/json' } })
      .then(function (res) {
        return res.json().then(function (body) { return { ok: res.ok, body: body }; });
      })
      .then(function (res) {
        if (res.ok && res.body.redirect_url) {
          window.location.href = res.body.redirect_url;
          return;
        }
        note.textContent = res.ok ? res.body.message
          : (res.body.error && res.body.error.message) || 'Something went wrong. Please try again.';
        note.hidden = false;
        button.disabled = false;
        if (res.ok) form.reset();
      })
      .catch(function () { form.submit(); });
  });
})();
</script>
`
	return Embed{ActionURL: action, HTML: b.String(), Script: script}
}
