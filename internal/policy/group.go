package policy

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/wiz"
)

// Group is one policy group file, resolved against the dictionary and catalog.
type Group struct {
	Name        string                     `json:"name"`
	File        string                     `json:"file"`
	Description string                     `json:"description,omitempty"`
	TagNames    []string                   `json:"tags"` // dictionary spelling
	Tags        []*TagDef                  `json:"-"`
	Targets     map[model.Cloud][]Target   `json:"targets"`
	Filters     map[model.Cloud]NameFilter `json:"filters,omitempty"`
	Overrides   map[model.Cloud]*Override  `json:"overrides,omitempty"`
}

// Stem is the file name without ".json", accepted by --group.
func (g *Group) Stem() string { return strings.TrimSuffix(g.File, ".json") }

// Target is one display name from the group's target list.
type Target struct {
	Display string         `json:"display"`
	Entry   *catalog.Entry `json:"-"` // canonical entry (aliases followed)
}

func (t Target) MarshalJSON() ([]byte, error) {
	out := struct {
		Display     string              `json:"display"`
		AliasOf     string              `json:"alias_of,omitempty"`
		NativeTypes []string            `json:"native_types"`
		Taggable    catalog.Taggability `json:"taggable"`
	}{Display: t.Display, NativeTypes: t.Entry.NativeTypes, Taggable: t.Entry.Taggable}
	if !strings.EqualFold(t.Display, t.Entry.Display) {
		out.AliasOf = t.Entry.Display
	}
	return json.Marshal(out)
}

// NameFilter excludes resources whose name contains any listed string.
type NameFilter struct {
	DoesNotContain []string `json:"does_not_contain"`
}

// Excludes reports whether name contains a listed string, ignoring case.
func (f NameFilter) Excludes(name string) bool {
	n := strings.ToLower(name)
	for _, s := range f.DoesNotContain {
		if strings.Contains(n, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// Override replaces the default missing-tag test for one cloud.
type Override struct {
	Missing *wiz.Query `json:"-"`
}

func (o *Override) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Missing json.RawMessage `json:"missing"`
	}{o.Missing.Raw})
}

// TypeKey identifies a resource type by cloud and canonical display name.
type TypeKey struct {
	Cloud   model.Cloud
	Display string
}

// Set is a loaded, valid policy. It is never modified after Load returns.
type Set struct {
	Dict    *Dictionary
	Groups  []*Group // sorted by name
	ByType  map[TypeKey][]*Group
	Hash    string // sha256 over the canonical JSON of every file
	Catalog *catalog.Catalog
}

// Select returns the groups matching any selector. A selector is a group
// name or file stem, ignoring case, and may use path.Match globs.
func (s *Set) Select(selectors []string) ([]*Group, error) {
	if len(selectors) == 0 {
		return s.Groups, nil
	}
	var out []*Group
	picked := map[*Group]bool{}
	for _, sel := range selectors {
		pat := strings.ToLower(strings.TrimSpace(sel))
		hit := false
		for _, g := range s.Groups {
			if matchName(pat, g.Name) || matchName(pat, g.Stem()) {
				hit = true
				if !picked[g] {
					picked[g] = true
					out = append(out, g)
				}
			}
		}
		if !hit {
			return nil, fmt.Errorf("no policy group matches %q", sel)
		}
	}
	// Keep the set's order regardless of selector order.
	ordered := out[:0:0]
	for _, g := range s.Groups {
		if picked[g] {
			ordered = append(ordered, g)
		}
	}
	return ordered, nil
}

func matchName(pattern, name string) bool {
	n := strings.ToLower(name)
	if pattern == n {
		return true
	}
	ok, err := path.Match(pattern, n)
	return err == nil && ok
}

// groupFile is the on-disk JSON shape, decoded with unknown fields rejected.
type groupFile struct {
	Name        *string                 `json:"name"`
	Description *string                 `json:"description"`
	Tags        []string                `json:"tags"`
	Target      map[string][]string     `json:"target"`
	Filters     map[string]filterFile   `json:"filters"`
	Override    map[string]overrideFile `json:"override"`
}

type filterFile struct {
	Name *struct {
		DoesNotContain []string `json:"DOES_NOT_CONTAIN"`
	} `json:"name"`
}

type overrideFile struct {
	All     json.RawMessage `json:"all"`
	Missing json.RawMessage `json:"missing"`
}
