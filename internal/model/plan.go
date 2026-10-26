package model

import (
	"cmp"
	"slices"
	"time"
)

// Action is what a change does to a tag. tagherd never removes tags.
type Action string

const (
	ActionAdd    Action = "ADD"
	ActionUpdate Action = "UPDATE"
)

// Route is how a change is written.
type Route string

const (
	RouteDirect     Route = "direct"     // cloud tagging API (DeploymentType Manual)
	RouteTerraform  Route = "terraform"  // patch and pull request (DeploymentType Automation)
	RouteUnroutable Route = "unroutable" // Terraform target could not be found
	RouteUnwritable Route = "unwritable" // type is scanned but never written
)

// Change is one proposed tag write.
type Change struct {
	ResourceID string `json:"resource_id"`
	Cloud      Cloud  `json:"cloud"`
	Scope      string `json:"scope"`
	Region     string `json:"region,omitempty"`
	Type       string `json:"type"`
	NativeType string `json:"native_type"`
	Name       string `json:"name,omitempty"`
	// Tag is the key written: the key as found for an update, the
	// dictionary name for an add. Kind says which GCP mechanism holds it;
	// GCP label writers lowercase the key and value.
	Tag    string  `json:"tag"`
	Kind   TagKind `json:"kind,omitempty"`
	Action Action  `json:"action"`
	Old    string  `json:"old,omitempty"`
	New    string  `json:"new"`
	// Rule is the value source: set, defaults:<level>, parent or
	// builtin:<name>.
	Rule  string `json:"rule"`
	Route Route  `json:"route"`
	// Expect is the value seen at plan time, the write precondition. With
	// ADD the tag was absent.
	Expect string `json:"expect"`
	// Repo is the repository the resource's SCM, Org and Repo tags name,
	// set on Terraform changes when all three are present.
	Repo *RepoRef `json:"repo,omitempty"`
}

// RepoRef names a Terraform repository, as the resource's tags give it.
type RepoRef struct {
	SCM  string `json:"scm"`
	Org  string `json:"org"`
	Repo string `json:"repo"`
}

func (r RepoRef) String() string { return r.SCM + "/" + r.Org + "/" + r.Repo }

// Unresolved is a finding the plan leaves open, and why.
type Unresolved struct {
	Finding
	Reason string `json:"reason"`
}

// PlanSummary counts what a plan holds.
type PlanSummary struct {
	Resources  int           `json:"resources"` // resources with at least one change
	Changes    int           `json:"changes"`
	ByRoute    map[Route]int `json:"changes_by_route"`
	ByCloud    map[Cloud]int `json:"changes_by_cloud"`
	Unresolved int           `json:"unresolved"`
	// SkippedGrace counts resources created within --grace.
	SkippedGrace int               `json:"skipped_grace"`
	Unreadable   []UnreadableScope `json:"unreadable_scopes"`
	Warnings     []string          `json:"warnings,omitempty"`
}

// Plan is the reviewable output of tagherd plan.
type Plan struct {
	ID         string       `json:"id"`
	Created    time.Time    `json:"created"`
	PolicyHash string       `json:"policy_hash"`
	Grace      string       `json:"grace"`
	Scopes     []string     `json:"scopes"` // cloud/scope, every scope listed
	Summary    PlanSummary  `json:"summary"`
	Changes    []Change     `json:"changes"`
	Unresolved []Unresolved `json:"unresolved"`
}

// SortChanges orders changes by cloud, scope, type, resource and tag.
func SortChanges(cs []Change) {
	slices.SortFunc(cs, func(a, b Change) int {
		return cmp.Or(
			cmp.Compare(a.Cloud, b.Cloud),
			cmp.Compare(a.Scope, b.Scope),
			cmp.Compare(a.Type, b.Type),
			cmp.Compare(a.ResourceID, b.ResourceID),
			cmp.Compare(a.Tag, b.Tag),
		)
	})
}
