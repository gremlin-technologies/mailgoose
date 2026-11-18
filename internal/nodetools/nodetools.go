// Package nodetools gives tests the web package (web/) with its npm packages
// installed. Several test packages need it, and `go test ./...` runs them at
// once, so the install happens under a lock.
package nodetools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// bins are the tools the tests run; if any is missing, `npm ci` runs.
var bins = []string{"redocly", "openapi-typescript", "tsc", "eslint", "vitest", "vite"}

// Web returns the web package's absolute directory with its npm packages
// installed. Without npm the test skips locally and fails in CI, where
// GitHub's runners have Node.
func Web(t testing.TB) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	web := filepath.Join(filepath.Dir(file), "..", "..", "web")
	if _, err := exec.LookPath("npm"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("npm isn't installed; CI needs Node to check the spec, the TypeScript client and the web app")
		}
		t.Skip("npm isn't installed")
	}
	if installed(web) {
		return web
	}

	sum := sha256.Sum256([]byte(web))
	unlock, err := lock(filepath.Join(os.TempDir(), "mailgoose-npm-"+hex.EncodeToString(sum[:8])+".lock"))
	if err != nil {
		t.Fatalf("lock for npm ci: %v", err)
	}
	defer unlock()
	if installed(web) { // another package installed while we waited
		return web
	}
	cmd := exec.CommandContext(t.Context(), "npm", "ci", "--no-audit", "--no-fund")
	cmd.Dir = web
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("npm ci: %v\n%s", err, out)
	}
	return web
}

func installed(web string) bool {
	for _, b := range bins {
		if _, err := os.Stat(filepath.Join(web, "node_modules", ".bin", b)); err != nil {
			return false
		}
	}
	return true
}
