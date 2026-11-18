package cli

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// annotationOperation names the OpenAPI operationId a command calls. Local
// commands (config, version) are marked with operationLocal instead. The
// parity check (MG-04) compares this registry with api/openapi.yaml.
const (
	annotationOperation = "mailgoose.operation"
	operationLocal      = "-"
)

// calls marks cmd as calling the API operation op, and returns it.
func calls(op string, cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationOperation] = op
	return cmd
}

// local marks cmd as not calling the API, and returns it.
func local(cmd *cobra.Command) *cobra.Command { return calls(operationLocal, cmd) }

// CommandInfo is one runnable command in the registry.
type CommandInfo struct {
	// Command is the command path without the program name, such as "config show".
	Command string `json:"command"`
	// Operation is the OpenAPI operationId it calls, or "" for a local command.
	Operation string `json:"operation,omitempty"`
	// Declared is false when the command doesn't say what it calls; tests fail on it.
	Declared bool `json:"-"`
}

// Registry lists every runnable command under root, sorted by path.
func Registry(root *cobra.Command) []CommandInfo {
	var out []CommandInfo
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			walk(sub)
		}
		if !c.Runnable() || c == root {
			return
		}
		op, ok := c.Annotations[annotationOperation]
		info := CommandInfo{Command: strings.TrimPrefix(c.CommandPath(), root.Name()+" "), Declared: ok}
		if op != operationLocal {
			info.Operation = op
		}
		out = append(out, info)
	}
	walk(root)
	sort.Slice(out, func(i, j int) bool { return out[i].Command < out[j].Command })
	return out
}
