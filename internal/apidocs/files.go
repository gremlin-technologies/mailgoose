package apidocs

import (
	"os"
	"path/filepath"

	"github.com/gremlin-technologies/mailgoose/internal/cli"
)

// Dir is where the generated docs live, from the repository root.
const Dir = "docs/api"

// Files renders every generated doc from the spec at specPath: the API
// reference, llms.txt, and the agent guide that `mailgoose docs` prints. The
// map's keys are file names inside Dir.
func Files(specPath string) (map[string]string, error) {
	doc, err := os.ReadFile(specPath) //nolint:gosec // a path in the repository
	if err != nil {
		return nil, err
	}
	spec, err := Parse(doc)
	if err != nil {
		return nil, err
	}
	cmds := Commands{}
	for _, c := range cli.NewApp().Commands() {
		for _, op := range c.Operations {
			cmds[op] = append(cmds[op], c.Command)
		}
	}
	ref, err := Reference(spec, cmds)
	if err != nil {
		return nil, err
	}
	llms, err := LLMsTxt(spec, cmds)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"reference.md":   ref,
		"llms.txt":       llms,
		"agent-guide.md": cli.AgentGuide,
	}, nil
}

// Write renders the docs and writes them under root/Dir.
func Write(root string) error {
	files, err := Files(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		return err
	}
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			return err
		}
	}
	return nil
}
