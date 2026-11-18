package apigen_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/nodetools"
)

// These tests are how CI checks the OpenAPI pipeline: the spec lints, and the
// generated Go types and TypeScript client match it. `make generate` fixes
// the last two; `make lint-api` shows lint problems.

// root is the repository root, absolute so commands can run from any directory.
var root = func() string {
	abs, err := filepath.Abs("../../..")
	if err != nil {
		panic(err)
	}
	return abs
}()

func TestGoTypesUpToDate(t *testing.T) {
	// Point the config's output at a temporary file; -o doesn't override it.
	cfg, err := os.ReadFile(filepath.Join(root, "api/oapi-codegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	out := filepath.Join(tmp, "apigen.gen.go")
	committed := []byte("output: internal/api/apigen/apigen.gen.go")
	if !bytes.Contains(cfg, committed) {
		t.Fatalf("api/oapi-codegen.yaml should contain %q", committed)
	}
	cfg = bytes.Replace(cfg, committed, []byte("output: "+out), 1)
	cfgPath := filepath.Join(tmp, "oapi-codegen.yaml")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, root, "go", "tool", "oapi-codegen", "-config", cfgPath, "api/openapi.yaml")
	same(t, out, filepath.Join(root, "internal/api/apigen/apigen.gen.go"))
}

func TestSpecLints(t *testing.T) {
	web := nodetools.Web(t)
	run(t, root, filepath.Join(web, "node_modules/.bin/redocly"), "lint", "api/openapi.yaml", "--config", "api/redocly.yaml")
}

func TestTypeScriptClientUpToDate(t *testing.T) {
	web := nodetools.Web(t)
	out := filepath.Join(t.TempDir(), "schema.d.ts")
	run(t, web, "node_modules/.bin/openapi-typescript", "../api/openapi.yaml", "--output", out)
	same(t, out, filepath.Join(web, "src/api/schema.d.ts"))
	run(t, web, "node_modules/.bin/tsc", "--noEmit")
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func same(t *testing.T, generated, committed string) {
	t.Helper()
	want, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(committed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is out of date with api/openapi.yaml; run `make generate` and commit the result", committed)
	}
}
