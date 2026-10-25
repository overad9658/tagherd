// Package values loads the files that supply tag values to a plan:
// defaults.yaml, ownership.yaml, synonyms.yaml and --set flags. Every value
// is checked against the tag dictionary when it is loaded, so the plan
// builder only ever sees values that may be written.
package values

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
)

// readFile returns the file's YAML document, or nil for an empty path or
// a file holding only comments.
func readFile(path string) (*yaml.Node, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(data)
}

func parse(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Tag == "!!null" {
		return nil, nil
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: must be a mapping", doc.Content[0].Line)
	}
	return doc.Content[0], nil
}

// entry is one key and its value node, in file order.
type entry struct {
	key  string
	line int
	val  *yaml.Node
}

func entries(n *yaml.Node) []entry {
	var out []entry
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, entry{key: n.Content[i].Value, line: n.Content[i].Line, val: n.Content[i+1]})
	}
	return out
}

// stringMap decodes a mapping of scalars.
func stringMap(n *yaml.Node) ([]entry, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: must be a mapping of tag to value", n.Line)
	}
	es := entries(n)
	for _, e := range es {
		if e.val.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: %s must be a single value", e.line, e.key)
		}
	}
	return es, nil
}

// cloudOf parses a top-level cloud key in any case.
func cloudOf(e entry) (model.Cloud, error) {
	c, err := model.ParseCloud(e.key)
	if err != nil {
		return "", fmt.Errorf("line %d: %w", e.line, err)
	}
	return c, nil
}

// accept checks one tag and value against the dictionary and returns both
// in dictionary spelling.
func accept(dict *policy.Dictionary, tag, value string) (string, string, error) {
	def, ok := dict.Lookup(tag)
	if !ok {
		return "", "", fmt.Errorf("%s is not a tag in %s", tag, policy.DictionaryFile)
	}
	v, ok := def.Accept(value)
	if !ok {
		return "", "", fmt.Errorf("%s=%q is not a valid value%s", def.Name, value, allowedHint(def))
	}
	return def.Name, v, nil
}

func allowedHint(def *policy.TagDef) string {
	switch {
	case def.Enumerated():
		return " (want one of " + strings.Join(def.Allowed, ", ") + ")"
	case def.Name == policy.AdminEmail:
		return " (want an email address)"
	}
	return ""
}

// ParseSet reads --set Tag=Value flags. A tag may be set once.
func ParseSet(flags []string, dict *policy.Dictionary) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range flags {
		tag, value, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("--set %q: want Tag=Value", f)
		}
		name, v, err := accept(dict, strings.TrimSpace(tag), value)
		if err != nil {
			return nil, fmt.Errorf("--set: %w", err)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("--set: %s is set more than once", name)
		}
		out[name] = v
	}
	return out, nil
}
