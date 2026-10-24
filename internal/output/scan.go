package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/overad9658/tagherd/internal/model"
)

// ScanReport is a scan's result as rendered.
type ScanReport struct {
	Summary  model.Summary   `json:"summary"`
	Findings []model.Finding `json:"findings"`
}

// markdownFindingLimit caps the findings listed in markdown, which is meant
// for PR comments and CI logs.
const markdownFindingLimit = 50

var findingHeaders = []string{"cloud", "scope", "region", "group", "type", "id", "name", "finding", "tag", "current", "proposed", "fixable"}

// RenderScan writes a scan report. With summaryOnly, only compliance
// percentages are shown.
func RenderScan(w io.Writer, opts Options, rep ScanReport, summaryOnly bool) error {
	switch opts.Format {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if summaryOnly {
			return enc.Encode(struct {
				Summary model.Summary `json:"summary"`
			}{rep.Summary})
		}
		return enc.Encode(rep)
	case NDJSON:
		enc := json.NewEncoder(w)
		if !summaryOnly {
			for _, f := range rep.Findings {
				if err := enc.Encode(f); err != nil {
					return err
				}
			}
		}
		return enc.Encode(struct {
			Summary model.Summary `json:"summary"`
		}{rep.Summary})
	case CSV:
		return scanCSV(w, rep, summaryOnly)
	case Markdown:
		return scanMarkdown(w, rep, summaryOnly)
	default:
		return scanTable(w, rep, summaryOnly, opts.Color)
	}
}

func scanCSV(w io.Writer, rep ScanReport, summaryOnly bool) error {
	cw := csv.NewWriter(w)
	if summaryOnly {
		_ = cw.Write([]string{"dimension", "key", "cloud", "evaluated", "compliant", "percent"})
		for _, r := range rateRows(rep.Summary.Compliance) {
			_ = cw.Write(r)
		}
	} else {
		_ = cw.Write(findingHeaders)
		for _, f := range rep.Findings {
			_ = cw.Write([]string{
				string(f.Cloud), f.Scope, f.Region, f.Group, f.Type, f.ID, f.Name,
				string(f.Code), f.Tag, f.Current, f.Proposed, strconv.FormatBool(f.Fixable),
			})
		}
	}
	cw.Flush() // csv.Writer keeps the first write error; Error reports it
	return cw.Error()
}

// rateRows flattens compliance tables for csv: dimension, key, cloud, ...
func rateRows(c model.ComplianceTables) [][]string {
	row := func(dim string, r model.Rate) []string {
		return []string{dim, r.Key, string(r.Cloud), strconv.Itoa(r.Evaluated), strconv.Itoa(r.Compliant), pct(r.Percent)}
	}
	out := [][]string{row("overall", c.Overall)}
	for _, r := range c.ByGroup {
		out = append(out, row("group", r))
	}
	for _, r := range c.ByCloud {
		out = append(out, row("cloud", r))
	}
	for _, r := range c.ByScope {
		out = append(out, row("scope", r))
	}
	return out
}

func scanTable(w io.Writer, rep ScanReport, summaryOnly, color bool) error {
	var sections []Rows
	if !summaryOnly && len(rep.Findings) > 0 {
		rows := Rows{Headers: []string{"CLOUD", "SCOPE", "TYPE", "NAME", "TAG", "FINDING", "CURRENT", "PROPOSED"}}
		for _, f := range rep.Findings {
			rows.Rows = append(rows.Rows, []string{
				string(f.Cloud), f.Scope, f.Type, nameOrID(f), f.Tag, string(f.Code), f.Current, f.Proposed,
			})
		}
		sections = append(sections, rows)
	}
	sections = append(sections, summarySections(rep.Summary)...)
	for i, s := range sections {
		if i > 0 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		if err := renderTable(w, s, color); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n"+totalsLine(rep.Summary)+"\n")
	return err
}

// summarySections are the compliance, unreadable and coverage tables shared
// by table and markdown output.
func summarySections(s model.Summary) []Rows {
	c := s.Compliance
	rate := func(key string, r model.Rate) []string {
		return []string{key, strconv.Itoa(r.Evaluated), strconv.Itoa(r.Compliant), pct(r.Percent) + "%"}
	}
	group := Rows{Headers: []string{"GROUP", "EVALUATED", "COMPLIANT", "COMPLIANCE"}}
	for _, r := range c.ByGroup {
		group.Rows = append(group.Rows, rate(r.Key, r))
	}
	cloud := Rows{Headers: []string{"CLOUD", "EVALUATED", "COMPLIANT", "COMPLIANCE"}}
	for _, r := range c.ByCloud {
		cloud.Rows = append(cloud.Rows, rate(r.Key, r))
	}
	scope := Rows{Headers: []string{"SCOPE", "EVALUATED", "COMPLIANT", "COMPLIANCE"}}
	for _, r := range c.ByScope {
		scope.Rows = append(scope.Rows, rate(string(r.Cloud)+" "+r.Key, r))
	}
	out := []Rows{group, cloud, scope}
	if len(s.Unreadable) > 0 {
		u := Rows{Headers: []string{"UNREADABLE SCOPE", "ERROR"}}
		for _, x := range s.Unreadable {
			u.Rows = append(u.Rows, []string{string(x.Cloud) + " " + x.Scope, x.Error})
		}
		out = append(out, u)
	}
	if len(s.ByTag) > 0 {
		t := Rows{Headers: []string{"TAG", "FINDINGS"}}
		for _, k := range sortedKeys(s.ByTag) {
			t.Rows = append(t.Rows, []string{k, strconv.Itoa(s.ByTag[k])})
		}
		out = append(out, t)
	}
	return out
}

func totalsLine(s model.Summary) string {
	r := s.Resources
	line := fmt.Sprintf("%s compliant (%d of %d evaluated). %d discovered, %d out of scope, %d filtered, %d not taggable.",
		pct(s.Compliance.Overall.Percent)+"%", r.Compliant, r.Evaluated, r.Discovered, r.OutOfScope, r.Filtered, r.NotTaggable)
	if n := len(s.Unreadable); n > 0 {
		line += fmt.Sprintf(" %d scope(s) unreadable.", n)
	}
	if n := len(s.Unmapped); n > 0 {
		line += fmt.Sprintf(" %d coverage gap(s).", n)
	}
	if len(s.OverrideFallback) > 0 {
		line += " Override fallback: " + strings.Join(s.OverrideFallback, "; ") + "."
	}
	return line
}

func scanMarkdown(w io.Writer, rep ScanReport, summaryOnly bool) error {
	var b strings.Builder
	b.WriteString("## tagherd scan\n\n" + totalsLine(rep.Summary) + "\n")
	for _, s := range summarySections(rep.Summary) {
		b.WriteString("\n" + markdownTable(s.Headers, s.Rows))
	}
	if !summaryOnly && len(rep.Findings) > 0 {
		shown := rep.Findings[:min(len(rep.Findings), markdownFindingLimit)]
		_, _ = fmt.Fprintf(&b, "\n### Findings (%d of %d)\n\n", len(shown), len(rep.Findings))
		rows := make([][]string, len(shown))
		for i, f := range shown {
			rows[i] = []string{string(f.Cloud), f.Scope, f.Type, nameOrID(f), f.Tag, string(f.Code), f.Current, f.Proposed}
		}
		b.WriteString(markdownTable([]string{"Cloud", "Scope", "Type", "Name", "Tag", "Finding", "Current", "Proposed"}, rows))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func nameOrID(f model.Finding) string {
	if f.Name != "" {
		return f.Name
	}
	return f.ID
}

func pct(p float64) string { return strconv.FormatFloat(p, 'f', 1, 64) }

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
