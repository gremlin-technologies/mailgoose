// Command gen writes docs/api from api/openapi.yaml (make docs). Run it
// from the repository root.
package main

import (
	"fmt"
	"os"

	"github.com/gremlin-technologies/mailgoose/internal/apidocs"
)

func main() {
	if err := apidocs.Write("."); err != nil {
		fmt.Fprintln(os.Stderr, "apidocs:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", apidocs.Dir)
}
