// Package catalog maps the policy's display names ("AWS RDS PostgreSQL Instance")
// to provider resource types, and records whether each type can hold tags.
package catalog

import (
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/overad9658/tagherd/internal/model"
)

//go:embed catalog.yaml
var embedded []byte

// Taggability says whether tagherd can write tags to a type.
type Taggability string

const (
	Taggable    Taggability = "true"
	NotTaggable Taggability = "false"
	// Partial types are scanned but never written, for example labels fixed at creation.
	Partial Taggability = "partial"
)

func (t *Taggability) UnmarshalYAML(n *yaml.Node) error {
	switch v := Taggability(strings.ToLower(n.Value)); v {
	case Taggable, NotTaggable, Partial:
		*t = v
		return nil
	}
	return fmt.Errorf("line %d: taggable must be true, false or partial, got %q", n.Line, n.Value)
}

// NativeTypes is one or more provider types. YAML accepts a string or a list.
type NativeTypes []string

func (nt *NativeTypes) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*nt = NativeTypes{n.Value}
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*nt = list
	return nil
}

// Matcher is a predicate on one resource attribute. YAML accepts a scalar
// (equals), a list (equals any) or a map with contains / not_contains.
type Matcher struct {
	Equals      []string `yaml:"equals" json:"equals,omitempty"`
	Contains    string   `yaml:"contains" json:"contains,omitempty"`
	NotContains string   `yaml:"not_contains" json:"not_contains,omitempty"`
}

func (m *Matcher) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		m.Equals = []string{n.Value}
		return nil
	case yaml.SequenceNode:
		return n.Decode(&m.Equals)
	}
	type plain Matcher
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*m = Matcher(p)
	if len(m.Equals) == 0 && m.Contains == "" && m.NotContains == "" {
		return fmt.Errorf("line %d: empty matcher", n.Line)
	}
	return nil
}

// Match reports whether an attribute value satisfies the matcher, ignoring case.
func (m Matcher) Match(value string) bool {
	v := strings.ToLower(value)
	if len(m.Equals) > 0 {
		hit := false
		for _, e := range m.Equals {
			if strings.EqualFold(e, value) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if m.Contains != "" && !strings.Contains(v, strings.ToLower(m.Contains)) {
		return false
	}
	if m.NotContains != "" && strings.Contains(v, strings.ToLower(m.NotContains)) {
		return false
	}
	return true
}

// Entry is one display name in the catalog.
type Entry struct {
	Display     string             `yaml:"display" json:"display"`
	Cloud       model.Cloud        `yaml:"cloud" json:"cloud"`
	NativeTypes NativeTypes        `yaml:"native_type" json:"native_types,omitempty"`
	Match       map[string]Matcher `yaml:"match" json:"match,omitempty"`
	Taggable    Taggability        `yaml:"taggable" json:"taggable"`
	WriteScope  string             `yaml:"write_scope" json:"write_scope,omitempty"`
	// AliasOf names another entry this one shares its classification with.
	AliasOf string `yaml:"alias_of" json:"alias_of,omitempty"`
	// Discovery is how the type is listed; see DiscoveryMode.
	Discovery string `yaml:"discovery" json:"discovery,omitempty"`
	Note      string `yaml:"note" json:"note,omitempty"`
}

// Discovery modes.
const (
	DiscoverBulk = "bulk" // the cloud's inventory source (the default)
	DiscoverList = "list" // a per-service list adapter, named after "list:"
	DiscoverNone = "none" // no adapter yet: reported as UNMAPPED_TYPE
)

// DiscoveryMode returns how the type is listed, and for DiscoverList the
// adapter's name.
func (e *Entry) DiscoveryMode() (mode, adapter string) {
	switch {
	case e.Discovery == "" || e.Discovery == DiscoverBulk:
		return DiscoverBulk, ""
	case e.Discovery == DiscoverNone:
		return DiscoverNone, ""
	case strings.HasPrefix(e.Discovery, DiscoverList+":"):
		return DiscoverList, strings.TrimPrefix(e.Discovery, DiscoverList+":")
	}
	return "", ""
}

// Catalog is the loaded, validated set of entries.
type Catalog struct {
	Entries  []*Entry // sorted by cloud, then display
	byKey    map[key]*Entry
	byNative map[nativeKey][]*Entry // canonical entries only
}

type nativeKey struct {
	cloud  model.Cloud
	native string // lower-cased
}

type key struct {
	cloud   model.Cloud
	display string // lower-cased
}

// Default returns the catalog embedded in the binary.
func Default() (*Catalog, error) { return Parse(embedded) }

// Parse decodes and validates catalog YAML.
func Parse(data []byte) (*Catalog, error) {
	var entries []*Entry
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	c := &Catalog{Entries: entries, byKey: make(map[key]*Entry, len(entries)), byNative: map[nativeKey][]*Entry{}}
	var errs []error
	for _, e := range entries {
		if !slices.Contains(model.Clouds, e.Cloud) {
			errs = append(errs, fmt.Errorf("%q: cloud must be AWS, Azure, GCP or OCI", e.Display))
		}
		if !strings.HasPrefix(e.Display, e.Cloud.Prefix()) {
			errs = append(errs, fmt.Errorf("%q: display name must start with %q", e.Display, e.Cloud.Prefix()))
		}
		if e.Taggable == "" {
			errs = append(errs, fmt.Errorf("%q: taggable is required", e.Display))
		}
		if e.AliasOf == "" && len(e.NativeTypes) == 0 {
			errs = append(errs, fmt.Errorf("%q: native_type is required", e.Display))
		}
		if e.AliasOf != "" && (len(e.NativeTypes) > 0 || len(e.Match) > 0 || e.Discovery != "") {
			errs = append(errs, fmt.Errorf("%q: an alias takes no native_type, match or discovery", e.Display))
		}
		if mode, adapter := e.DiscoveryMode(); mode == "" || (mode == DiscoverList && adapter == "") {
			errs = append(errs, fmt.Errorf("%q: discovery must be bulk, none or list:<adapter>, got %q", e.Display, e.Discovery))
		}
		k := key{e.Cloud, strings.ToLower(e.Display)}
		if _, dup := c.byKey[k]; dup {
			errs = append(errs, fmt.Errorf("%q: duplicate entry", e.Display))
		}
		c.byKey[k] = e
		for _, nt := range e.NativeTypes {
			nk := nativeKey{e.Cloud, strings.ToLower(nt)}
			c.byNative[nk] = append(c.byNative[nk], e)
		}
	}
	for _, e := range entries {
		if e.AliasOf == "" {
			continue
		}
		target, ok := c.byKey[key{e.Cloud, strings.ToLower(e.AliasOf)}]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("%q: alias_of %q is not in the catalog", e.Display, e.AliasOf))
		case target.AliasOf != "":
			errs = append(errs, fmt.Errorf("%q: alias_of %q is itself an alias", e.Display, e.AliasOf))
		}
	}
	errs = append(errs, checkShared(entries)...)
	if len(errs) > 0 {
		return nil, fmt.Errorf("catalog: %w", errors.Join(errs...))
	}
	sort.Slice(c.Entries, func(i, j int) bool {
		a, b := c.Entries[i], c.Entries[j]
		if a.Cloud != b.Cloud {
			return a.Cloud < b.Cloud
		}
		return a.Display < b.Display
	})
	return c, nil
}

// checkShared requires a match on every entry that shares a native type with
// another, so classification can never pick two entries without a predicate.
func checkShared(entries []*Entry) []error {
	users := map[string][]*Entry{}
	for _, e := range entries {
		for _, nt := range e.NativeTypes {
			users[nt] = append(users[nt], e)
		}
	}
	var errs []error
	for nt, es := range users {
		if len(es) < 2 {
			continue
		}
		for _, e := range es {
			if len(e.Match) == 0 {
				errs = append(errs, fmt.Errorf("%q shares %s with %d other entries and needs a match", e.Display, nt, len(es)-1))
			}
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errs
}

// Lookup finds an entry by cloud and display name, ignoring case.
func (c *Catalog) Lookup(cloud model.Cloud, display string) (*Entry, bool) {
	e, ok := c.byKey[key{cloud, strings.ToLower(display)}]
	return e, ok
}

// Canonical follows an alias to the entry that owns the classification.
func (c *Catalog) Canonical(e *Entry) *Entry {
	if e.AliasOf == "" {
		return e
	}
	t, _ := c.Lookup(e.Cloud, e.AliasOf)
	return t
}

// ErrAmbiguous means more than one catalog entry matched a resource.
var ErrAmbiguous = errors.New("ambiguous catalog match")

// Classify returns the entry for a discovered resource: the one canonical
// entry whose native type matches and whose predicates all hold. It returns
// nil when the resource is of no catalog type.
func (c *Catalog) Classify(cloud model.Cloud, nativeType string, attrs map[string]string) (*Entry, error) {
	var hit *Entry
	for _, e := range c.byNative[nativeKey{cloud, strings.ToLower(nativeType)}] {
		if !e.matches(attrs) {
			continue
		}
		if hit != nil {
			return nil, fmt.Errorf("%w: %s resource matches %q and %q", ErrAmbiguous, nativeType, hit.Display, e.Display)
		}
		hit = e
	}
	return hit, nil
}

func (e *Entry) matches(attrs map[string]string) bool {
	for attr, m := range e.Match {
		if !m.Match(attrs[attr]) {
			return false
		}
	}
	return true
}
