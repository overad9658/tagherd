package model

import (
	"cmp"
	"slices"
	"time"
)

// TagKind is how a cloud stores a tag. GCP has two mechanisms.
type TagKind string

const (
	KindTag   TagKind = "tag"   // AWS and Azure tags
	KindLabel TagKind = "label" // GCP labels: lowercase keys and values
	KindRMTag TagKind = "rmtag" // GCP Resource Manager tag bindings
)

// Tag is one key/value as found on a resource, with its original spelling.
type Tag struct {
	Key   string  `json:"key" yaml:"key"`
	Value string  `json:"value" yaml:"value"`
	Kind  TagKind `json:"kind,omitempty" yaml:"kind,omitempty"`
}

// Resource is one discovered cloud resource, normalized across clouds.
type Resource struct {
	Cloud      Cloud      `json:"cloud"`
	Scope      string     `json:"scope"` // account, subscription or project ID
	Region     string     `json:"region,omitempty"`
	Type       string     `json:"type,omitempty"` // display name, set by classification
	NativeType string     `json:"native_type"`
	ID         string     `json:"id"` // ARN, ARM ID or CAI full name
	Name       string     `json:"name"`
	Created    *time.Time `json:"created,omitempty"`
	Tags       []Tag      `json:"tags"`
	Parent     string     `json:"parent,omitempty"` // resource group or project, for inheritance
	// Attrs feed the catalog's discriminators (engine, kind, sku, ...).
	Attrs map[string]string `json:"-"`
}

// Code is a finding type.
type Code string

const (
	MissingTag            Code = "MISSING_TAG"
	EmptyValue            Code = "EMPTY_VALUE"
	InvalidValue          Code = "INVALID_VALUE"
	CaseMismatch          Code = "CASE_MISMATCH"
	DuplicateKey          Code = "DUPLICATE_KEY"
	InvalidDeploymentType Code = "INVALID_DEPLOYMENT_TYPE"
	NotTaggable           Code = "NOT_TAGGABLE"
	UnmappedType          Code = "UNMAPPED_TYPE"
)

// Class groups codes for --fail-on.
type Class string

const (
	ClassMissing Class = "missing"
	ClassInvalid Class = "invalid"
	ClassNone    Class = "" // never affects the exit code
)

// Class returns the --fail-on class a code belongs to.
func (c Code) Class() Class {
	switch c {
	case MissingTag, EmptyValue:
		return ClassMissing
	case InvalidValue, CaseMismatch, DuplicateKey, InvalidDeploymentType:
		return ClassInvalid
	}
	return ClassNone
}

// Finding is one policy result for one resource and tag. Its JSON is the
// shape shown in the CLI spec.
type Finding struct {
	Cloud    Cloud  `json:"cloud"`
	Scope    string `json:"scope"`
	Region   string `json:"region,omitempty"`
	Group    string `json:"group,omitempty"` // several groups are joined by ", "
	Type     string `json:"type"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Code     Code   `json:"finding"`
	Tag      string `json:"tag,omitempty"`
	Current  string `json:"current,omitempty"`
	Proposed string `json:"proposed,omitempty"`
	Fixable  bool   `json:"fixable"`
}

// SortFindings orders findings by cloud, scope, type, ID, tag and code, so
// the same input always prints the same output.
func SortFindings(fs []Finding) {
	slices.SortFunc(fs, func(a, b Finding) int {
		return cmp.Or(
			cmp.Compare(a.Cloud, b.Cloud),
			cmp.Compare(a.Scope, b.Scope),
			cmp.Compare(a.Type, b.Type),
			cmp.Compare(a.ID, b.ID),
			cmp.Compare(a.Tag, b.Tag),
			cmp.Compare(a.Code, b.Code),
		)
	})
}
