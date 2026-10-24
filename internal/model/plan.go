package model

import "time"

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

// Change is one proposed tag write. Built by the plan command (phase 4).
type Change struct {
	ResourceID string `json:"resource_id"`
	Cloud      Cloud  `json:"cloud"`
	Scope      string `json:"scope"`
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Action     Action `json:"action"`
	Old        string `json:"old,omitempty"`
	New        string `json:"new"`
	Rule       string `json:"rule"` // set | defaults | parent | builtin:<name>
	Route      Route  `json:"route"`
	Expect     string `json:"expect"` // value seen at plan time; the write precondition
}

// Plan is the reviewable output of tagherd plan.
type Plan struct {
	ID         string    `json:"id"`
	Created    time.Time `json:"created"`
	PolicyHash string    `json:"policy_hash"`
	Scopes     []string  `json:"scopes"`
	Changes    []Change  `json:"changes"`
	Unresolved []Finding `json:"unresolved"`
}
