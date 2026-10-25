package values

import (
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
)

// OwningOrg is the tag the ownership map fills.
const OwningOrg = "OwningOrg"

// Ownership maps scopes to an OwningOrg value:
//
//	aws:
//	  "111122223333": Platform
//	gcp:
//	  my-project: DevOps
type Ownership struct {
	scopes map[model.Cloud]map[string]string
}

// LoadOwnership reads an ownership file. An empty path gives an empty map.
func LoadOwnership(path string, dict *policy.Dictionary) (*Ownership, error) {
	n, err := readFile(path)
	if err != nil {
		return nil, err
	}
	o, err := ownershipFrom(n, dict)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return o, nil
}

// ParseOwnership is LoadOwnership over bytes, for tests.
func ParseOwnership(data []byte, dict *policy.Dictionary) (*Ownership, error) {
	n, err := parse(data)
	if err != nil {
		return nil, err
	}
	return ownershipFrom(n, dict)
}

func ownershipFrom(n *yaml.Node, dict *policy.Dictionary) (*Ownership, error) {
	o := &Ownership{scopes: map[model.Cloud]map[string]string{}}
	if n == nil {
		return o, nil
	}
	if _, ok := dict.Lookup(OwningOrg); !ok {
		return nil, fmt.Errorf("%s is not a tag in %s", OwningOrg, policy.DictionaryFile)
	}
	for _, ce := range entries(n) {
		cloud, err := cloudOf(ce)
		if err != nil {
			return nil, err
		}
		if o.scopes[cloud] != nil {
			return nil, fmt.Errorf("line %d: %s appears twice", ce.line, ce.key)
		}
		es, err := stringMap(ce.val)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ce.key, err)
		}
		m := map[string]string{}
		for _, se := range es {
			_, v, err := accept(dict, OwningOrg, se.val.Value)
			if err != nil {
				return nil, fmt.Errorf("line %d: %s.%s: %w", se.line, ce.key, se.key, err)
			}
			if _, dup := m[se.key]; dup {
				return nil, fmt.Errorf("line %d: %s.%s appears twice", se.line, ce.key, se.key)
			}
			m[se.key] = v
		}
		o.scopes[cloud] = m
	}
	return o, nil
}

// Lookup returns the scope's OwningOrg.
func (o *Ownership) Lookup(cloud model.Cloud, scope string) (string, bool) {
	v, ok := o.scopes[cloud][scope]
	return v, ok
}
