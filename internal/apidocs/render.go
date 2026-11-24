package apidocs

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Header starts every generated file, so nobody edits one by hand.
const generatedNote = "Generated from api/openapi.yaml by `make docs`. Don't edit it by hand."

// tagTitles names each tag's section, in the order the reference shows them.
// A tag missing here still gets a section, after these, under its own name.
var tagTitles = []struct{ tag, title string }{
	{"health", "Health"},
	{"auth", "Signing in"},
	{"account", "Account"},
	{"api-keys", "API keys"},
	{"plan", "Plan and billing"},
	{"credits", "Credits"},
	{"domains", "Sending domains"},
	{"contacts", "Contacts"},
	{"tags", "Tags"},
	{"contact-fields", "Contact fields"},
	{"imports", "Imports and exports"},
	{"images", "Images"},
	{"emails", "Emails"},
	{"campaigns", "Campaigns"},
	{"forms", "Signup forms"},
	{"admin", "Operator tools (mailgoose staff only)"},
}

type section struct {
	tag, title string
	ops        []Operation
}

func (s *Spec) sections() ([]section, error) {
	ops, err := s.Operations()
	if err != nil {
		return nil, err
	}
	byTag := map[string][]Operation{}
	var order []string
	for _, op := range ops {
		tag := "other"
		if len(op.Tags) > 0 {
			tag = op.Tags[0]
		}
		if _, ok := byTag[tag]; !ok {
			order = append(order, tag)
		}
		byTag[tag] = append(byTag[tag], op)
	}
	var out []section
	seen := map[string]bool{}
	for _, t := range tagTitles {
		if len(byTag[t.tag]) > 0 {
			out = append(out, section{t.tag, t.title, byTag[t.tag]})
			seen[t.tag] = true
		}
	}
	for _, tag := range order {
		if !seen[tag] {
			out = append(out, section{tag, tag, byTag[tag]})
		}
	}
	return out, nil
}

// Reference renders the API reference as Markdown.
func Reference(s *Spec, cmds Commands) (string, error) {
	secs, err := s.sections()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# %s reference\n\n", s.Info.Title)
	w("<!-- %s -->\n\n", generatedNote)
	w("Version %s. Every operation has a matching `mailgoose` CLI command, named under it. ", s.Info.Version)
	w("For a walk-through rather than a reference, run `mailgoose docs` or read [the agent guide](agent-guide.md).\n\n")
	w("%s\n", strings.TrimSpace(s.Info.Description))
	if len(s.Servers) > 0 {
		w("\nServers:\n\n")
		for _, sv := range s.Servers {
			w("- %s: `%s`\n", sv.Description, sv.URL)
		}
	}

	w("\n## Contents\n\n")
	for _, sec := range secs {
		w("- [%s](#%s)\n", sec.title, sectionAnchor(sec.tag))
	}
	w("- [Schemas](#schemas)\n")

	for _, sec := range secs {
		w("\n<a id=\"%s\"></a>\n\n## %s\n", sectionAnchor(sec.tag), sec.title)
		for _, op := range sec.ops {
			s.writeOperation(&b, op, cmds)
		}
	}

	w("\n<a id=\"schemas\"></a>\n\n## Schemas\n")
	if err := s.writeSchemas(&b); err != nil {
		return "", err
	}
	return b.String(), nil
}

func sectionAnchor(tag string) string { return "tag-" + tag }

// OpAnchor is the anchor of an operation's heading in the reference.
func OpAnchor(op Operation) string { return "op-" + strings.ToLower(op.OperationID) }

func (s *Spec) writeOperation(b *strings.Builder, op Operation, cmds Commands) {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	w("\n<a id=\"%s\"></a>\n\n### %s\n\n", OpAnchor(op), op.Summary)
	w("`%s %s`\n\n", op.Method, op.Path)
	w("- Operation: `%s`\n", op.OperationID)
	if c := cmds.of(op.OperationID); c != "" {
		w("- CLI: %s\n", c)
	}
	if op.RequiredScope != "" {
		w("- Needs a `%s`-scope key\n", op.RequiredScope)
	}
	if d := strings.TrimSpace(op.Description); d != "" {
		w("\n%s\n", d)
	}

	if len(op.Parameters) > 0 {
		w("\n| Parameter | In | Type | Required | Description |\n| --- | --- | --- | --- | --- |\n")
		for _, p := range op.Parameters {
			p = s.param(p)
			req := ""
			if p.Required {
				req = "yes"
			}
			w("| `%s` | %s | %s | %s | %s |\n", p.Name, p.In, typeOf(p.Schema), req, oneLine(p.Description))
		}
	}

	if op.RequestBody != nil {
		for _, ct := range sortedKeys(op.RequestBody.Content) {
			mt := op.RequestBody.Content[ct]
			req := "optional"
			if op.RequestBody.Required {
				req = "required"
			}
			w("\nRequest body (`%s`, %s): %s\n", ct, req, typeOf(mt.Schema))
		}
	}

	if op.Responses.Kind == yaml.MappingNode {
		w("\nResponses:\n\n")
		for i := 0; i+1 < len(op.Responses.Content); i += 2 {
			code := op.Responses.Content[i].Value
			var r Response
			if err := op.Responses.Content[i+1].Decode(&r); err != nil {
				continue
			}
			r = s.response(r)
			line := fmt.Sprintf("- `%s`: %s", code, oneLine(r.Description))
			if mt, ok := r.Content["application/json"]; ok && mt.Schema != nil {
				line += " " + typeOf(mt.Schema)
			}
			w("%s\n", strings.TrimRight(line, " "))
		}
	}
}

func (s *Spec) writeSchemas(b *strings.Builder) error {
	w := func(format string, args ...any) { fmt.Fprintf(b, format, args...) }
	n := s.Components.Schemas
	for i := 0; i+1 < len(n.Content); i += 2 {
		name := n.Content[i].Value
		var sc Schema
		if err := n.Content[i+1].Decode(&sc); err != nil {
			return fmt.Errorf("schema %s: %w", name, err)
		}
		w("\n<a id=\"%s\"></a>\n\n### %s\n\n", schemaAnchor(name), name)
		if d := strings.TrimSpace(sc.Description); d != "" {
			w("%s\n\n", d)
		}
		if sc.Properties.Kind != yaml.MappingNode || len(sc.Properties.Content) == 0 {
			w("Type: %s\n", typeOf(&sc))
			continue
		}
		required := map[string]bool{}
		for _, r := range sc.Required {
			required[r] = true
		}
		w("| Field | Type | Required | Description |\n| --- | --- | --- | --- |\n")
		for j := 0; j+1 < len(sc.Properties.Content); j += 2 {
			field := sc.Properties.Content[j].Value
			var p Schema
			if err := sc.Properties.Content[j+1].Decode(&p); err != nil {
				return fmt.Errorf("schema %s.%s: %w", name, field, err)
			}
			req := ""
			if required[field] {
				req = "yes"
			}
			w("| `%s` | %s | %s | %s |\n", field, oneLine(typeOf(&p)), req, oneLine(p.Description))
		}
	}
	return nil
}

// LLMsTxt renders the llms.txt index (https://llmstxt.org): a short summary
// of mailgoose, then one link per guide and per operation, with paths
// relative to the docs/api directory it lives in.
func LLMsTxt(s *Spec, cmds Commands) (string, error) {
	secs, err := s.sections()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# mailgoose\n\n")
	w("> Small email marketing service: design emails (blocks or HTML), send them to a list of contacts, and collect subscribers with signup forms. One REST API, and a `mailgoose` CLI with a command for every endpoint, so an AI agent can do everything a person can in the web app.\n\n")
	w("<!-- %s -->\n\n", generatedNote)
	w("Authenticate with an API key as `Authorization: Bearer mg_…`, or set `MAILGOOSE_API_KEY` for the CLI. ")
	w("Add `--json` to any CLI command for machine-readable output. ")
	w("Errors are `{\"error\": {\"code\", \"message\", \"hint\"}}` with a stable `code`. ")
	w("A campaign launch needs a confirmation code that a person says yes to first; see the agent guide.\n")

	w("\n## Guides\n\n")
	w("- [Agent guide](agent-guide.md): signing in with an API key, building an email from blocks, checks, test sends, and the launch confirmation. Also printed by `mailgoose docs`.\n")
	w("- [Moving from Mailchimp](agent-guide.md#moving-from-mailchimp): bringing contacts, tags, emails and signup forms over from a Mailchimp export or a connected Mailchimp account, the preview the person says yes to, and what doesn't come over.\n")
	w("- [API reference](reference.md): every operation, its parameters, responses and schemas.\n")

	for _, sec := range secs {
		w("\n## %s\n\n", sec.title)
		for _, op := range sec.ops {
			line := fmt.Sprintf("- [%s](reference.md#%s): `%s %s`", op.Summary, OpAnchor(op), op.Method, op.Path)
			if c := cmds.of(op.OperationID); c != "" {
				line += ", CLI " + c
			}
			if d := firstSentence(op.Description); d != "" {
				line += ". " + d
			}
			w("%s\n", line)
		}
	}
	return b.String(), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
