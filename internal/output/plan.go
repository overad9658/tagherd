package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/overad9658/tagherd/internal/model"
)

// planTrailer is the last NDJSON line: everything but the changes.
type planTrailer struct {
	ID         string             `json:"id"`
	PolicyHash string             `json:"policy_hash"`
	Summary    model.PlanSummary  `json:"summary"`
	Unresolved []model.Unresolved `json:"unresolved"`
}

var changeHeaders = []string{"cloud", "scope", "region", "type", "id", "name", "tag", "kind", "action", "old", "new", "rule", "route"}

// RenderPlan writes a plan in any format. The plan file itself is always
// the JSON form.
func RenderPlan(w io.Writer, opts Options, p model.Plan) error {
	switch opts.Format {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	case NDJSON:
		enc := json.NewEncoder(w)
		for _, c := range p.Changes {
			if err := enc.Encode(c); err != nil {
				return err
			}
		}
		return enc.Encode(planTrailer{ID: p.ID, PolicyHash: p.PolicyHash, Summary: p.Summary, Unresolved: p.Unresolved})
	case CSV:
		cw := csv.NewWriter(w)
		_ = cw.Write(changeHeaders)
		for _, c := range p.Changes {
			_ = cw.Write([]string{string(c.Cloud), c.Scope, c.Region, c.Type, c.ResourceID, c.Name, c.Tag,
				string(c.Kind), string(c.Action), c.Old, c.New, c.Rule, string(c.Route)})
		}
		cw.Flush()
		return cw.Error()
	case Markdown:
		var b strings.Builder
		b.WriteString("## tagherd plan\n\n" + planTotals(p) + "\n")
		if len(p.Changes) > 0 {
			shown := p.Changes[:min(len(p.Changes), markdownFindingLimit)]
			_, _ = fmt.Fprintf(&b, "\n### Changes (%d of %d)\n\n", len(shown), len(p.Changes))
			b.WriteString(markdownTable([]string{"Cloud", "Scope", "Type", "Name", "Tag", "Action", "Old", "New", "Rule", "Route"}, changeRows(shown)))
		}
		if len(p.Unresolved) > 0 {
			shown := p.Unresolved[:min(len(p.Unresolved), markdownFindingLimit)]
			_, _ = fmt.Fprintf(&b, "\n### Unresolved (%d of %d)\n\n", len(shown), len(p.Unresolved))
			b.WriteString(markdownTable([]string{"Cloud", "Scope", "Type", "Name", "Tag", "Finding", "Current", "Reason"}, unresolvedRows(shown)))
		}
		_, err := io.WriteString(w, b.String())
		return err
	default:
		var sections []Rows
		if len(p.Changes) > 0 {
			sections = append(sections, Rows{
				Headers: []string{"CLOUD", "SCOPE", "TYPE", "NAME", "TAG", "ACTION", "OLD", "NEW", "RULE", "ROUTE"},
				Rows:    changeRows(p.Changes),
			})
		}
		if len(p.Unresolved) > 0 {
			sections = append(sections, Rows{
				Headers: []string{"CLOUD", "SCOPE", "TYPE", "NAME", "TAG", "FINDING", "CURRENT", "UNRESOLVED BECAUSE"},
				Rows:    unresolvedRows(p.Unresolved),
			})
		}
		for _, u := range p.Summary.Unreadable {
			if len(sections) == 0 {
				sections = append(sections, Rows{})
			}
			last := &sections[len(sections)-1]
			last.Footer = append(last.Footer, fmt.Sprintf("unreadable: %s %s: %s", u.Cloud, u.Scope, u.Error))
		}
		for i, s := range sections {
			if i > 0 {
				if _, err := io.WriteString(w, "\n"); err != nil {
					return err
				}
			}
			if err := renderTable(w, s, opts.Color); err != nil {
				return err
			}
		}
		if len(sections) > 0 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, planTotals(p)+"\n")
		return err
	}
}

func changeRows(cs []model.Change) [][]string {
	out := make([][]string, len(cs))
	for i, c := range cs {
		tag := c.Tag
		if c.Kind == model.KindRMTag {
			tag = "tag:" + tag
		}
		out[i] = []string{string(c.Cloud), c.Scope, c.Type, firstNonEmpty(c.Name, c.ResourceID), tag,
			string(c.Action), c.Old, c.New, c.Rule, string(c.Route)}
	}
	return out
}

func unresolvedRows(us []model.Unresolved) [][]string {
	out := make([][]string, len(us))
	for i, u := range us {
		out[i] = []string{string(u.Cloud), u.Scope, u.Type, nameOrID(u.Finding), u.Tag, string(u.Code), u.Current, u.Reason}
	}
	return out
}

// planTotals is the one-line summary: changes by route, then what is left.
func planTotals(p model.Plan) string {
	s := p.Summary
	line := fmt.Sprintf("%d change(s) to %d resource(s)", s.Changes, s.Resources)
	var routes []string
	for _, r := range []model.Route{model.RouteDirect, model.RouteTerraform, model.RouteUnroutable, model.RouteUnwritable} {
		if n := s.ByRoute[r]; n > 0 {
			routes = append(routes, fmt.Sprintf("%d %s", n, r))
		}
	}
	if len(routes) > 0 {
		line += ": " + strings.Join(routes, ", ")
	}
	line += fmt.Sprintf(". %d unresolved.", s.Unresolved)
	if s.SkippedGrace > 0 {
		line += fmt.Sprintf(" %d resource(s) skipped, created within %s.", s.SkippedGrace, p.Grace)
	}
	if n := len(s.Unreadable); n > 0 {
		line += fmt.Sprintf(" %d scope(s) unreadable.", n)
	}
	return line
}
