package values

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
)

// Defaults are tag values by cloud, scope, group or type:
//
//	aws:
//	  "*": { Exposure: Internal }               # every AWS scope
//	  "111122223333": { Brand: Acme }           # one scope
//	  groups:
//	    Database: { DataClassification: Confidential }
//	  types:
//	    AWS S3 Bucket: { DataClassification: Internal }
//
// The most specific match wins: type, then group, then scope, then cloud.
// "groups" and "types" are reserved words, never scope IDs.
type Defaults struct {
	clouds map[model.Cloud]*cloudDefaults
}

type cloudDefaults struct {
	all    map[string]string            // "*"
	scopes map[string]map[string]string // by scope ID
	groups map[string]map[string]string // by group name
	types  map[string]map[string]string // by display name
}

// Default levels, as they appear in a change's rule (defaults:<level>).
const (
	LevelType  = "type"
	LevelGroup = "group"
	LevelScope = "scope"
	LevelCloud = "cloud"
)

// LoadDefaults reads a defaults file. An empty path gives no defaults.
func LoadDefaults(path string, set *policy.Set) (*Defaults, error) {
	n, err := readFile(path)
	if err != nil {
		return nil, err
	}
	d, err := defaultsFrom(n, set)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// ParseDefaults is LoadDefaults over bytes, for tests.
func ParseDefaults(data []byte, set *policy.Set) (*Defaults, error) {
	n, err := parse(data)
	if err != nil {
		return nil, err
	}
	return defaultsFrom(n, set)
}

func defaultsFrom(n *yaml.Node, set *policy.Set) (*Defaults, error) {
	d := &Defaults{clouds: map[model.Cloud]*cloudDefaults{}}
	if n == nil {
		return d, nil
	}
	for _, ce := range entries(n) {
		cloud, err := cloudOf(ce)
		if err != nil {
			return nil, err
		}
		if d.clouds[cloud] != nil {
			return nil, fmt.Errorf("line %d: %s appears twice", ce.line, ce.key)
		}
		if ce.val.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("line %d: %s must be a mapping", ce.line, ce.key)
		}
		cd := &cloudDefaults{
			scopes: map[string]map[string]string{},
			groups: map[string]map[string]string{},
			types:  map[string]map[string]string{},
		}
		d.clouds[cloud] = cd
		for _, se := range entries(ce.val) {
			where := ce.key + "." + se.key
			switch strings.ToLower(se.key) {
			case "groups":
				if err := nested(se, where, set.Dict, cd.groups, func(name string) (string, error) {
					return groupName(set, name)
				}); err != nil {
					return nil, err
				}
			case "types":
				if err := nested(se, where, set.Dict, cd.types, func(name string) (string, error) {
					e, ok := set.Catalog.Lookup(cloud, name)
					if !ok {
						return "", fmt.Errorf("%s is not a %s type in the catalog", name, cloud)
					}
					return set.Catalog.Canonical(e).Display, nil
				}); err != nil {
					return nil, err
				}
			default:
				tags, err := tagValues(se, where, set.Dict)
				if err != nil {
					return nil, err
				}
				if se.key == "*" {
					cd.all = tags
				} else {
					cd.scopes[se.key] = tags
				}
			}
		}
	}
	return d, nil
}

// nested reads groups: or types: — a mapping of name to tag values.
func nested(e entry, where string, dict *policy.Dictionary, into map[string]map[string]string, resolve func(string) (string, error)) error {
	if e.val.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: %s must be a mapping of name to tag values", e.line, where)
	}
	for _, ne := range entries(e.val) {
		name, err := resolve(ne.key)
		if err != nil {
			return fmt.Errorf("line %d: %s: %w", ne.line, where, err)
		}
		if into[name] != nil {
			return fmt.Errorf("line %d: %s: %s appears twice", ne.line, where, name)
		}
		tags, err := tagValues(ne, where+"."+ne.key, dict)
		if err != nil {
			return err
		}
		into[name] = tags
	}
	return nil
}

// tagValues reads a mapping of tag to value, in dictionary spelling.
func tagValues(e entry, where string, dict *policy.Dictionary) (map[string]string, error) {
	es, err := stringMap(e.val)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	out := map[string]string{}
	for _, te := range es {
		tag, v, err := accept(dict, te.key, te.val.Value)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", te.line, where, err)
		}
		if _, dup := out[tag]; dup {
			return nil, fmt.Errorf("line %d: %s: %s appears twice", te.line, where, tag)
		}
		out[tag] = v
	}
	return out, nil
}

// groupName resolves a group by name or file stem, ignoring case.
func groupName(set *policy.Set, name string) (string, error) {
	for _, g := range set.Groups {
		if strings.EqualFold(g.Name, name) || strings.EqualFold(g.Stem(), name) {
			return g.Name, nil
		}
	}
	return "", fmt.Errorf("%s is not a policy group", name)
}

// Lookup returns the default for tag on a resource and the level it came
// from. groups are the resource's matched group names.
func (d *Defaults) Lookup(cloud model.Cloud, scope, typ string, groups []string, tag string) (value, level string, ok bool) {
	cd := d.clouds[cloud]
	if cd == nil {
		return "", "", false
	}
	if v, ok := cd.types[typ][tag]; ok {
		return v, LevelType, true
	}
	for _, g := range groups {
		if v, ok := cd.groups[g][tag]; ok {
			return v, LevelGroup, true
		}
	}
	if v, ok := cd.scopes[scope][tag]; ok {
		return v, LevelScope, true
	}
	if v, ok := cd.all[tag]; ok {
		return v, LevelCloud, true
	}
	return "", "", false
}
