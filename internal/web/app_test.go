package web_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/nodetools"
	"github.com/gremlin-technologies/mailgoose/internal/web"
)

// These tests are how CI checks the web app (web/): ESLint, the TypeScript
// type check, the Vitest tests and a production build, which this package
// then serves. Run them alone with `make web-check`.

func npm(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("npm", append([]string{"run", "-s"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("npm run %v: %v\n%s", args, err, out)
	}
}

func TestWebAppLints(t *testing.T) {
	npm(t, nodetools.Web(t), "lint")
}

func TestWebAppTypeChecks(t *testing.T) {
	npm(t, nodetools.Web(t), "typecheck")
}

func TestWebAppTests(t *testing.T) {
	npm(t, nodetools.Web(t), "test")
}

// The build must produce an app this package can serve, with every script
// and stylesheet index.html names.
func TestWebAppBuildsAndServes(t *testing.T) {
	dir := nodetools.Web(t)
	out := t.TempDir()
	npm(t, dir, "build", "--", "--outDir", out, "--emptyOutDir")

	h, err := web.New(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	assets := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindAllStringSubmatch(string(index), -1)
	if len(assets) < 2 {
		t.Fatalf("index.html should load a script and a stylesheet from /assets, found %v", assets)
	}
	for _, path := range append([]string{"/favicon.svg"}, firsts(assets)...) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", path, rec.Code)
		}
	}
}

func firsts(matches [][]string) []string {
	var out []string
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}
