package values

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/overad9658/tagherd/internal/policy"
)

// deploymentType is never corrected: its invalid values are left alone.
const deploymentType = "DeploymentType"

// Synonyms correct invalid values: tag -> value found -> dictionary value.
//
//	Environment:
//	  prod: Production
type Synonyms struct {
	tags map[string]map[string]string // dictionary tag -> lower-cased found value -> value
}

// LoadSynonyms reads a synonyms file. An empty path gives an empty map.
func LoadSynonyms(path string, dict *policy.Dictionary) (*Synonyms, error) {
	n, err := readFile(path)
	if err != nil {
		return nil, err
	}
	s, err := synonymsFrom(n, dict)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// ParseSynonyms is LoadSynonyms over bytes, for tests.
func ParseSynonyms(data []byte, dict *policy.Dictionary) (*Synonyms, error) {
	n, err := parse(data)
	if err != nil {
		return nil, err
	}
	return synonymsFrom(n, dict)
}

func synonymsFrom(n *yaml.Node, dict *policy.Dictionary) (*Synonyms, error) {
	s := &Synonyms{tags: map[string]map[string]string{}}
	if n == nil {
		return s, nil
	}
	for _, te := range entries(n) {
		def, ok := dict.Lookup(te.key)
		if !ok {
			return nil, fmt.Errorf("line %d: %s is not a tag in %s", te.line, te.key, policy.DictionaryFile)
		}
		if def.Name == deploymentType {
			return nil, fmt.Errorf("line %d: %s has no synonyms: its invalid values are never corrected", te.line, def.Name)
		}
		if s.tags[def.Name] != nil {
			return nil, fmt.Errorf("line %d: %s appears twice", te.line, def.Name)
		}
		es, err := stringMap(te.val)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", te.key, err)
		}
		m := map[string]string{}
		for _, ve := range es {
			_, v, err := accept(dict, def.Name, ve.val.Value)
			if err != nil {
				return nil, fmt.Errorf("line %d: %s.%s: %w", ve.line, def.Name, ve.key, err)
			}
			found := strings.ToLower(strings.TrimSpace(ve.key))
			if _, dup := m[found]; dup {
				return nil, fmt.Errorf("line %d: %s.%s appears twice (values found ignore case)", ve.line, def.Name, ve.key)
			}
			m[found] = v
		}
		s.tags[def.Name] = m
	}
	return s, nil
}

// Lookup returns the correction for a value found on tag, ignoring case.
func (s *Synonyms) Lookup(tag, found string) (string, bool) {
	v, ok := s.tags[tag][strings.ToLower(strings.TrimSpace(found))]
	return v, ok
}
