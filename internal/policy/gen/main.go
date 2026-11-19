// Command gen writes the web app's copy of the policy versions,
// web/src/policyVersions.gen.ts. `make generate` runs it from the repo root.
package main

import (
	"log"
	"os"

	"github.com/gremlin-technologies/mailgoose/internal/policy"
)

func main() {
	if err := os.WriteFile(policy.WebFile, policy.TypeScript(), 0o644); err != nil { //nolint:gosec // G306: a source file, readable by all.
		log.Fatal(err)
	}
}
