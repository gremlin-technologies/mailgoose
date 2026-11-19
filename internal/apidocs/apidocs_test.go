package apidocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../.."

// TestDocsUpToDate fails when docs/api no longer matches api/openapi.yaml,
// the CLI's commands or the agent guide. Run `make docs` and commit.
func TestDocsUpToDate(t *testing.T) {
	files, err := Files(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(root, Dir, name))
		if err != nil {
			t.Errorf("%s/%s is missing; run make docs", Dir, name)
			continue
		}
		if string(got) != want {
			t.Errorf("%s/%s is out of date; run make docs and commit it", Dir, name)
		}
	}
}

func TestReferenceCoversEveryOperation(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Files(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ops, err := spec.Operations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) < 50 {
		t.Fatalf("only %d operations read", len(ops))
	}
	for _, op := range ops {
		anchor := `<a id="` + OpAnchor(op) + `"></a>`
		if !strings.Contains(files["reference.md"], anchor) {
			t.Errorf("reference has no section for %s", op.OperationID)
		}
		if !strings.Contains(files["llms.txt"], "(reference.md#"+OpAnchor(op)+")") {
			t.Errorf("llms.txt doesn't link %s", op.OperationID)
		}
	}
	for _, want := range []string{
		"### Launch a campaign, after confirmation",
		"- CLI: `mailgoose campaign launch`",
		"- Needs a `full`-scope key",
		"| `limit` | query | integer |",
		`<a id="schema-contact"></a>`,
	} {
		if !strings.Contains(files["reference.md"], want) {
			t.Errorf("reference lacks %q", want)
		}
	}
	if !strings.HasPrefix(files["llms.txt"], "# mailgoose\n\n> ") {
		t.Errorf("llms.txt doesn't start with a title and a summary, as llmstxt.org asks")
	}
}
