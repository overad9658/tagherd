// Package provider defines the interfaces each cloud adapter implements. The
// engine and apply code see only these, never a cloud SDK.
package provider

import (
	"context"
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
	// Writer returns a tag writer for a scope, with write credentials.
	Writer(ctx context.Context, s Scope) (TagWriter, error)
}

// Target is one resource to read or write tags on.
type Target struct {
	ID         string // ARN, ARM ID or CAI full name
	NativeType string
	Region     string // AWS region or GCP location; empty for global
}

// TagWrite merges tags into one resource. On GCP each tag's Kind says
// whether it is a label (already lowercase) or a Resource Manager tag.
type TagWrite struct {
	Target Target
	Tags   []model.Tag
}

// TagWriter reads and writes tags on resources in one scope, with write
// credentials. Calls take many resources so adapters can batch them.
type TagWriter interface {
	// Actor is the identity writes are made as, for the audit log.
	Actor(ctx context.Context) (string, error)
	// Read returns every tag on each target, by ID, as discovery reports
	// them (GCP: labels and effective Resource Manager tags). A target
	// that could not be read has an entry in errs instead.
	Read(ctx context.Context, targets []Target) (tags map[string][]model.Tag, errs map[string]error)
	// Write merges the tags into each resource and returns an error per
	// write that failed, by ID. It never removes a tag; a GCP Resource
	// Manager binding with another value for the same key is replaced.
	Write(ctx context.Context, writes []TagWrite) map[string]error
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
