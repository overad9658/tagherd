// Package provider defines the interfaces each cloud adapter implements. The
// engine and apply code see only these, never a cloud SDK.
package provider

import (
	"context"
	"errors"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
)

// Scope is one AWS account, Azure subscription or GCP project.
type Scope struct {
	Cloud model.Cloud `json:"cloud"`
	ID    string      `json:"id"`
	Name  string      `json:"name,omitempty"`
}

// Discovery is what one scope returned.
type Discovery struct {
	Resources []model.Resource
	// Uncovered are requested types neither bulk nor fallback discovery can
	// list in this scope; they are reported as UNMAPPED_TYPE.
	Uncovered []*catalog.Entry
	// Errors are parts of the scope that could not be read, such as one
	// type or one region, while the rest could. Each is reported as an
	// unreadable entry for the scope; the resources found are still used.
	Errors []error
}

// ErrWritesNotYet is returned by Writer until the tag writers land.
var ErrWritesNotYet = errors.New("tag writes are not implemented yet; they arrive in phase 5")

// Provider is one cloud's adapter.
type Provider interface {
	Cloud() model.Cloud
	// Scopes lists the scopes to scan. patterns are --account, --subscription
	// or --project globs, matched against ID or name; none selects all.
	Scopes(ctx context.Context, patterns []string) ([]Scope, error)
	// Discover lists resources of the given types in one scope. An error
	// marks the scope unreadable; the scan goes on with the others.
	Discover(ctx context.Context, s Scope, types []*catalog.Entry) (Discovery, error)
	// ParentTags returns the tags of the resource's parent: AWS account tags,
	// Azure resource group tags or GCP project labels (used from phase 4).
	ParentTags(ctx context.Context, r model.Resource) ([]model.Tag, error)
	// Writer returns a tag writer for a scope, with write credentials (phase 5).
	Writer(ctx context.Context, s Scope) (TagWriter, error)
}

// TagWriter reads and writes tags on resources in one scope.
type TagWriter interface {
	// Read returns the current values of keys, matched ignoring case. A key
	// that is absent is missing from the map.
	Read(ctx context.Context, id string, keys []string) (map[string]string, error)
	// Write merges tags into the resource. It never removes a tag.
	Write(ctx context.Context, id string, tags []model.Tag) error
}

// TypeSplit is a discovery request divided by how each type is listed.
type TypeSplit struct {
	Bulk      []*catalog.Entry
	List      map[string][]*catalog.Entry // by adapter name
	Uncovered []*catalog.Entry
}

// SplitTypes divides requested types by their catalog discovery mode. A
// list adapter the provider does not have (known returns false) leaves its
// types uncovered.
func SplitTypes(types []*catalog.Entry, known func(adapter string) bool) TypeSplit {
	out := TypeSplit{List: map[string][]*catalog.Entry{}}
	for _, t := range types {
		switch mode, adapter := t.DiscoveryMode(); {
		case mode == catalog.DiscoverBulk:
			out.Bulk = append(out.Bulk, t)
		case mode == catalog.DiscoverList && known(adapter):
			out.List[adapter] = append(out.List[adapter], t)
		default:
			out.Uncovered = append(out.Uncovered, t)
		}
	}
	return out
}

// NativeTypes returns the distinct native types of entries, sorted.
func NativeTypes(entries []*catalog.Entry) []string {
	var out []string
	for _, e := range entries {
		for _, nt := range e.NativeTypes {
			if !slices.Contains(out, nt) {
				out = append(out, nt)
			}
		}
	}
	sort.Strings(out)
	return out
}

// SelectScopes keeps the scopes whose ID or name matches any glob, ignoring
// case. No patterns keeps all.
func SelectScopes(scopes []Scope, patterns []string) []Scope {
	if len(patterns) == 0 {
		return scopes
	}
	var out []Scope
	for _, s := range scopes {
		if matchAny(patterns, s.ID) || (s.Name != "" && matchAny(patterns, s.Name)) {
			out = append(out, s)
		}
	}
	return out
}

func matchAny(patterns []string, s string) bool {
	s = strings.ToLower(s)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if ok, err := path.Match(p, s); p == s || (err == nil && ok) {
			return true
		}
	}
	return false
}
