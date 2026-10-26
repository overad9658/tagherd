package model

import "time"

// Outcome is what happened to one planned change.
type Outcome string

const (
	OutcomeWritten Outcome = "written" // the tag was written
	OutcomeNoop    Outcome = "noop"    // the tag already held the new value
	OutcomeStale   Outcome = "stale"   // the tag changed since the plan; skipped
	OutcomeFailed  Outcome = "failed"  // the write or a check failed
	OutcomePending Outcome = "pending" // Terraform route, not run (--only direct)
	OutcomeSkipped Outcome = "skipped" // unwritable; never applied

	// Terraform route outcomes. Noop and failed apply to it too.
	OutcomeProposed   Outcome = "proposed"   // in a patch file, and a pull request with --pr
	OutcomeUnroutable Outcome = "unroutable" // no place in code to make it; Detail says why
)

// Reasons a Terraform change is unroutable, the start of its Detail.
const (
	UnroutableMissingRepoTags = "missing_repo_tags"
	UnroutableNotAllowlisted  = "not_allowlisted"
	UnroutableNotInState      = "not_in_state"
	UnroutableNotInCode       = "not_in_code"
	UnroutableUnsupported     = "unsupported_expression"
)

// ChangeResult is one change and its outcome.
type ChangeResult struct {
	Change
	Outcome Outcome `json:"outcome"`
	Detail  string  `json:"detail,omitempty"`
	// Terraform says where a Terraform change was made, once its address
	// is known.
	Terraform *TerraformTarget `json:"terraform,omitempty"`
}

// TerraformTarget is where in code a Terraform change goes.
type TerraformTarget struct {
	Root    string `json:"root"`               // directory in the repository; "." is its top
	Address string `json:"address"`            // the resource's state address
	File    string `json:"file,omitempty"`     // the file edited, relative to the repository
	Block   string `json:"block,omitempty"`    // the block edited: the resource or the module call
	Patch   string `json:"patch,omitempty"`    // the patch file written
	PullURL string `json:"pull_url,omitempty"` // the pull request opened with --pr
}

// ApplySummary counts outcomes.
type ApplySummary struct {
	Written int `json:"written"`
	Noop    int `json:"noop"`
	Stale   int `json:"stale"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
	Skipped int `json:"skipped"`
	// Terraform route.
	Proposed   int `json:"proposed"`
	Unroutable int `json:"unroutable"`
}

// Add counts one outcome.
func (s *ApplySummary) Add(o Outcome) {
	switch o {
	case OutcomeWritten:
		s.Written++
	case OutcomeNoop:
		s.Noop++
	case OutcomeStale:
		s.Stale++
	case OutcomeFailed:
		s.Failed++
	case OutcomePending:
		s.Pending++
	case OutcomeSkipped:
		s.Skipped++
	case OutcomeProposed:
		s.Proposed++
	case OutcomeUnroutable:
		s.Unroutable++
	}
}

// ApplyReport is what tagherd apply prints.
type ApplyReport struct {
	PlanID  string         `json:"plan_id"`
	Summary ApplySummary   `json:"summary"`
	Results []ChangeResult `json:"results"`
	// Verify lists findings still open on the changed resources, when
	// --verify ran.
	Verify []Finding `json:"verify,omitempty"`
}

// AuditRecord is one line of tagherd-audit.ndjson: one attempted change. A
// Terraform change is attempted when tagherd edits its code; Detail then
// names the patch or pull request.
type AuditRecord struct {
	Time       time.Time `json:"time"`
	PlanID     string    `json:"plan_id"`
	Actor      string    `json:"actor"`
	Cloud      Cloud     `json:"cloud"`
	Scope      string    `json:"scope"`
	ResourceID string    `json:"resource_id"`
	Type       string    `json:"type"`
	Tag        string    `json:"tag"`
	Kind       TagKind   `json:"kind,omitempty"`
	Route      Route     `json:"route"`
	Old        string    `json:"old"` // the value found just before the write; the plan's for Terraform
	New        string    `json:"new"`
	Rule       string    `json:"rule"`
	Outcome    Outcome   `json:"outcome"`
	Detail     string    `json:"detail,omitempty"`
}
