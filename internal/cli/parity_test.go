package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
	"go.yaml.in/yaml/v3"
)

const (
	specPath      = "../../api/openapi.yaml"
	allowlistPath = "../../api/cli-parity-allowlist.yaml"
)

// TestCLIParity is the CLI parity check (MG-04): every operation in the API
// spec has a CLI command, unless api/cli-parity-allowlist.yaml says why not.
// CI runs it with the rest of `go test`.
func TestCLIParity(t *testing.T) {
	ops := specOperations(t)
	allow := allowlist(t)
	for _, problem := range checkParity(ops, NewApp().Commands(), allow) {
		t.Error(problem)
	}
}

// TestCLIParityFailsWithoutCommand removes a command from the registry and
// expects the check to name its operation.
func TestCLIParityFailsWithoutCommand(t *testing.T) {
	ops := specOperations(t)
	allow := allowlist(t)
	reg := NewApp().Commands()
	i := slices.IndexFunc(reg, func(c CommandInfo) bool { return c.Command == "status" })
	if i < 0 {
		t.Fatal("expected a status command")
	}
	removed := reg[i].Operation
	reg = slices.Delete(reg, i, i+1)

	problems := checkParity(ops, reg, allow)
	if len(problems) != 1 || !strings.Contains(problems[0], removed) {
		t.Fatalf("problems = %q, want one naming %s", problems, removed)
	}
}

func TestCLIParityAllowlistRules(t *testing.T) {
	ops := []string{"getThing", "postWebhook"}
	reg := []CommandInfo{{Command: "thing get", Operation: "getThing", Operations: []string{"getThing"}, Declared: true}}
	tests := []struct {
		name  string
		allow map[string]string
		want  []string
	}{
		{"web-only operation listed", map[string]string{"postWebhook": "Stripe calls it"}, nil},
		{"missing command", nil, []string{"postWebhook"}},
		{"entry without a reason", map[string]string{"postWebhook": " "}, []string{"postWebhook", "reason"}},
		{"entry that has a command", map[string]string{"postWebhook": "x", "getThing": "y"}, []string{"getThing", "has a command"}},
		{"entry not in the spec", map[string]string{"postWebhook": "x", "gone": "y"}, []string{"gone", "not in"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := strings.Join(checkParity(ops, reg, tt.allow), "\n")
			if tt.want == nil && problems != "" {
				t.Fatalf("unexpected problems:\n%s", problems)
			}
			for _, w := range tt.want {
				if !strings.Contains(problems, w) {
					t.Errorf("problems %q should mention %q", problems, w)
				}
			}
		})
	}
}

// checkParity returns one line per problem, naming the operation.
func checkParity(ops []string, reg []CommandInfo, allow map[string]string) []string {
	commands := map[string][]string{}
	for _, c := range reg {
		for _, op := range c.Operations {
			commands[op] = append(commands[op], c.Command)
		}
	}
	var problems []string
	inSpec := map[string]bool{}
	for _, op := range ops {
		inSpec[op] = true
		_, allowed := allow[op]
		if len(commands[op]) == 0 && !allowed {
			problems = append(problems, fmt.Sprintf("operation %s has no CLI command; add one (see calls() in registry.go) or list it with a reason in api/cli-parity-allowlist.yaml", op))
		}
	}
	for _, op := range sortedKeys(allow) {
		switch {
		case !inSpec[op]:
			problems = append(problems, fmt.Sprintf("api/cli-parity-allowlist.yaml lists %s, which is not in api/openapi.yaml; remove the entry", op))
		case len(commands[op]) > 0:
			problems = append(problems, fmt.Sprintf("api/cli-parity-allowlist.yaml lists %s, but it has a command (%s); remove the entry", op, strings.Join(commands[op], ", ")))
		case strings.TrimSpace(allow[op]) == "":
			problems = append(problems, fmt.Sprintf("api/cli-parity-allowlist.yaml lists %s without a reason", op))
		}
	}
	for op := range commands {
		if !inSpec[op] {
			problems = append(problems, fmt.Sprintf("command %s calls %s, which is not in api/openapi.yaml", strings.Join(commands[op], ", "), op))
		}
	}
	return problems
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func specOperations(t *testing.T) []string {
	t.Helper()
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for path, item := range model.Model.Paths.PathItems.FromOldest() {
		for method, op := range item.GetOperations().FromOldest() {
			if op.OperationId == "" {
				t.Fatalf("%s %s has no operationId", strings.ToUpper(method), path)
			}
			ops = append(ops, op.OperationId)
		}
	}
	if len(ops) == 0 {
		t.Fatal("the spec has no operations")
	}
	return ops
}

func allowlist(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	allow := map[string]string{}
	if err := yaml.Unmarshal(b, &allow); err != nil {
		t.Fatalf("%s: %v", allowlistPath, err)
	}
	return allow
}
