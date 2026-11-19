// Package apidocs turns api/openapi.yaml into the API reference (Markdown)
// and an llms.txt index for AI agents (MG-55). Both are generated, committed
// under docs/api, and a test fails when they fall behind the spec:
//
//	make docs
package apidocs

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Spec is the part of an OpenAPI 3.1 document the docs use.
type Spec struct {
	Info struct {
		Title       string `yaml:"title"`
		Version     string `yaml:"version"`
		Description string `yaml:"description"`
	} `yaml:"info"`
	Servers []struct {
		URL         string `yaml:"url"`
		Description string `yaml:"description"`
	} `yaml:"servers"`
	Paths      yaml.Node `yaml:"paths"`
	Components struct {
		Parameters map[string]Parameter `yaml:"parameters"`
		Responses  map[string]Response  `yaml:"responses"`
		Schemas    yaml.Node            `yaml:"schemas"`
	} `yaml:"components"`
}

// Operation is one method on one path.
type Operation struct {
	OperationID   string      `yaml:"operationId"`
	Summary       string      `yaml:"summary"`
	Description   string      `yaml:"description"`
	Tags          []string    `yaml:"tags"`
	RequiredScope string      `yaml:"x-required-scope"`
	Parameters    []Parameter `yaml:"parameters"`
	RequestBody   *struct {
		Required bool                 `yaml:"required"`
		Content  map[string]MediaType `yaml:"content"`
	} `yaml:"requestBody"`
	Responses yaml.Node `yaml:"responses"`

	// Filled in while reading.
	Method string `yaml:"-"`
	Path   string `yaml:"-"`
}

// Parameter is an inline parameter or a $ref to one.
type Parameter struct {
	Ref         string  `yaml:"$ref"`
	Name        string  `yaml:"name"`
	In          string  `yaml:"in"`
	Required    bool    `yaml:"required"`
	Description string  `yaml:"description"`
	Schema      *Schema `yaml:"schema"`
}

// Response is an inline response or a $ref to one.
type Response struct {
	Ref         string               `yaml:"$ref"`
	Description string               `yaml:"description"`
	Content     map[string]MediaType `yaml:"content"`
}

// MediaType is one content type of a body.
type MediaType struct {
	Schema *Schema `yaml:"schema"`
}

// Schema is the part of a JSON Schema the docs show.
type Schema struct {
	Ref         string    `yaml:"$ref"`
	Type        yaml.Node `yaml:"type"`
	Format      string    `yaml:"format"`
	Description string    `yaml:"description"`
	Enum        []any     `yaml:"enum"`
	Items       *Schema   `yaml:"items"`
	Required    []string  `yaml:"required"`
	Properties  yaml.Node `yaml:"properties"`
	OneOf       []*Schema `yaml:"oneOf"`
	AnyOf       []*Schema `yaml:"anyOf"`
	AllOf       []*Schema `yaml:"allOf"`
}

var methods = []string{"get", "post", "put", "patch", "delete"}

// Parse reads an OpenAPI document.
func Parse(doc []byte) (*Spec, error) {
	var s Spec
	if err := yaml.Unmarshal(doc, &s); err != nil {
		return nil, fmt.Errorf("parse openapi: %w", err)
	}
	if s.Paths.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse openapi: no paths")
	}
	return &s, nil
}

// Operations lists every operation in the order the spec has them.
func (s *Spec) Operations() ([]Operation, error) {
	var ops []Operation
	for i := 0; i+1 < len(s.Paths.Content); i += 2 {
		path := s.Paths.Content[i].Value
		var item map[string]yaml.Node
		if err := s.Paths.Content[i+1].Decode(&item); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, m := range methods {
			n, ok := item[m]
			if !ok {
				continue
			}
			var op Operation
			if err := n.Decode(&op); err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(m), path, err)
			}
			if op.OperationID == "" {
				return nil, fmt.Errorf("%s %s has no operationId", strings.ToUpper(m), path)
			}
			op.Method, op.Path = strings.ToUpper(m), path
			ops = append(ops, op)
		}
	}
	return ops, nil
}

// refName returns the last part of a $ref, such as "Contact".
func refName(ref string) string { return ref[strings.LastIndex(ref, "/")+1:] }

func (s *Spec) param(p Parameter) Parameter {
	if p.Ref != "" {
		if r, ok := s.Components.Parameters[refName(p.Ref)]; ok {
			return r
		}
	}
	return p
}

func (s *Spec) response(r Response) Response {
	if r.Ref != "" {
		if c, ok := s.Components.Responses[refName(r.Ref)]; ok {
			return c
		}
	}
	return r
}

// typeOf describes a schema's type in a few words, such as "string (uuid)",
// "array of Contact" or "string or null".
func typeOf(sc *Schema) string {
	if sc == nil {
		return ""
	}
	if sc.Ref != "" {
		return "[" + refName(sc.Ref) + "](#" + schemaAnchor(refName(sc.Ref)) + ")"
	}
	for _, alts := range [][]*Schema{sc.OneOf, sc.AnyOf, sc.AllOf} {
		if len(alts) > 0 {
			var parts []string
			for _, a := range alts {
				parts = append(parts, typeOf(a))
			}
			joiner := " or "
			if len(sc.AllOf) > 0 {
				joiner = " and "
			}
			return strings.Join(parts, joiner)
		}
	}
	var types []string
	switch sc.Type.Kind {
	case yaml.ScalarNode:
		types = []string{sc.Type.Value}
	case yaml.SequenceNode:
		for _, n := range sc.Type.Content {
			types = append(types, n.Value)
		}
	}
	for i, t := range types {
		switch {
		case t == "array" && sc.Items != nil:
			types[i] = "array of " + typeOf(sc.Items)
		case t == "string" && sc.Format != "":
			types[i] = "string (" + sc.Format + ")"
		}
	}
	out := strings.Join(types, " or ")
	if len(sc.Enum) > 0 {
		var vals []string
		for _, v := range sc.Enum {
			vals = append(vals, fmt.Sprintf("`%v`", v))
		}
		out += ": one of " + strings.Join(vals, ", ")
	}
	if out == "" {
		out = "any"
	}
	return out
}

func schemaAnchor(name string) string { return "schema-" + strings.ToLower(name) }

// oneLine folds a description onto one line, for tables.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

// firstSentence is a description's first sentence, for the llms.txt index.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

// Commands maps an operationId to the CLI command(s) that call it, such as
// "contacts list".
type Commands map[string][]string

func (c Commands) of(op string) string {
	cmds := append([]string(nil), c[op]...)
	sort.Strings(cmds)
	for i, cmd := range cmds {
		cmds[i] = "`mailgoose " + cmd + "`"
	}
	return strings.Join(cmds, ", ")
}
