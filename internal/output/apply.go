package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/overad9658/tagherd/internal/model"
)

// applyTrailer is the last NDJSON line: everything but the results.
type applyTrailer struct {
	PlanID  string             `json:"plan_id"`
	Summary model.ApplySummary `json:"summary"`
	Verify  []model.Finding    `json:"verify,omitempty"`
}

// RenderApply writes an apply report in any format.
func RenderApply(w io.Writer, opts Options, rep model.ApplyReport) error {
	rep.Results = nonNil(rep.Results)
	switch opts.Format {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case NDJSON:
		enc := json.NewEncoder(w)
		for _, r := range rep.Results {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
		return enc.Encode(applyTrailer{PlanID: rep.PlanID, Summary: rep.Summary, Verify: rep.Verify})
	case CSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"cloud", "scope", "type", "id", "name", "tag", "action", "old", "new", "route", "outcome", "detail",
			"repo", "address", "file", "patch", "pull_url"})
		for _, r := range rep.Results {
			var repo string
			if r.Repo != nil {
				repo = r.Repo.String()
			}
			tf := r.Terraform
			if tf == nil {
				tf = &model.TerraformTarget{}
			}
			_ = cw.Write([]string{string(r.Cloud), r.Scope, r.Type, r.ResourceID, r.Name, r.Tag, string(r.Action),
				r.Old, r.New, string(r.Route), string(r.Outcome), r.Detail, repo, tf.Address, tf.File, tf.Patch, tf.PullURL})
		}
		cw.Flush()
		return cw.Error()
	case Markdown:
		var b strings.Builder
		b.WriteString("## tagherd apply\n\n" + applyTotals(rep) + "\n")
		if len(rep.Results) > 0 {
			shown := rep.Results[:min(len(rep.Results), markdownFindingLimit)]
			_, _ = fmt.Fprintf(&b, "\n### Changes (%d of %d)\n\n", len(shown), len(rep.Results))
			b.WriteString(markdownTable([]string{"Cloud", "Scope", "Type", "Name", "Tag", "New", "Outcome", "Detail"}, resultRows(shown)))
		}
		if len(rep.Verify) > 0 {
			_, _ = fmt.Fprintf(&b, "\n### Still open after the writes (%d)\n\n", len(rep.Verify))
			b.WriteString(markdownTable([]string{"Cloud", "Scope", "Type", "Name", "Tag", "Finding", "Current"}, verifyRows(rep.Verify)))
		}
		_, err := io.WriteString(w, b.String())
		return err
	default:
		var sections []Rows
		if len(rep.Results) > 0 {
			sections = append(sections, Rows{Headers: []string{"CLOUD", "SCOPE", "TYPE", "NAME", "TAG", "NEW", "OUTCOME", "DETAIL"}, Rows: resultRows(rep.Results)})
		}
		if len(rep.Verify) > 0 {
			sections = append(sections, Rows{Headers: []string{"CLOUD", "SCOPE", "TYPE", "NAME", "TAG", "STILL OPEN", "CURRENT"}, Rows: verifyRows(rep.Verify)})
		}
		for _, s := range sections {
			if err := renderTable(w, s, opts.Color); err != nil {
				return err
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, applyTotals(rep)+"\n")
		return err
	}
}

func resultRows(rs []model.ChangeResult) [][]string {
	out := make([][]string, len(rs))
	for i, r := range rs {
		tag := r.Tag
		if r.Kind == model.KindRMTag {
			tag = "tag:" + tag
		}
		out[i] = []string{string(r.Cloud), r.Scope, r.Type, firstNonEmpty(r.Name, r.ResourceID), tag, r.New, string(r.Outcome), r.Detail}
	}
	return out
}

func verifyRows(fs []model.Finding) [][]string {
	out := make([][]string, len(fs))
	for i, f := range fs {
		out[i] = []string{string(f.Cloud), f.Scope, f.Type, nameOrID(f), f.Tag, string(f.Code), f.Current}
	}
	return out
}

func applyTotals(rep model.ApplyReport) string {
	s := rep.Summary
	line := fmt.Sprintf("%d written, %d already set, %d stale, %d failed.", s.Written, s.Noop, s.Stale, s.Failed)
	if s.Proposed > 0 || s.Unroutable > 0 {
		line += fmt.Sprintf(" Terraform: %d proposed, %d unroutable.", s.Proposed, s.Unroutable)
	}
	if s.Pending > 0 {
		line += fmt.Sprintf(" %d Terraform change(s) not run.", s.Pending)
	}
	if s.Skipped > 0 {
		line += fmt.Sprintf(" %d unwritable change(s) skipped.", s.Skipped)
	}
	if rep.Verify != nil {
		line += fmt.Sprintf(" Verify: %d finding(s) still open.", len(rep.Verify))
	}
	return line
}
