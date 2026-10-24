// Package provider defines the interfaces each cloud adapter implements. The
// engine and apply code see only these, never a cloud SDK.
package provider

import (
	"context"

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
