package model

import "time"

// Outcome is what happened to one planned change.
type Outcome string

const (
	OutcomeWritten Outcome = "written" // the tag was written
	OutcomeNoop    Outcome = "noop"    // the tag already held the new value
	OutcomeStale   Outcome = "stale"   // the tag changed since the plan; skipped
	OutcomeFailed  Outcome = "failed"  // the write or a check failed
	OutcomePending Outcome = "pending" // Terraform route, not applied yet
	OutcomeSkipped Outcome = "skipped" // unwritable or unroutable; never applied
)

// ChangeResult is one change and its outcome.
type ChangeResult struct {
	Change
	Outcome Outcome `json:"outcome"`
	Detail  string  `json:"detail,omitempty"`
}

// ApplySummary counts outcomes.
type ApplySummary struct {
	Written int `json:"written"`
	Noop    int `json:"noop"`
	Stale   int `json:"stale"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
	Skipped int `json:"skipped"`
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

// AuditRecord is one line of tagherd-audit.ndjson: one attempted change.
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
	Old        string    `json:"old"` // the value found just before the write
	New        string    `json:"new"`
	Rule       string    `json:"rule"`
	Outcome    Outcome   `json:"outcome"`
	Detail     string    `json:"detail,omitempty"`
}
