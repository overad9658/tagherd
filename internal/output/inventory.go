package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/overad9658/tagherd/internal/model"
)

// InventoryReport is what `tagherd inventory` prints.
type InventoryReport struct {
	Resources  []model.Resource        `json:"resources"`
	Unreadable []model.UnreadableScope `json:"unreadable_scopes"`
	Unmapped   []model.CoverageGap     `json:"unmapped_types"`
}

// inventoryTrailer is the last NDJSON line: everything but the resources.
type inventoryTrailer struct {
	Summary struct {
		Resources  int                     `json:"resources"`
		Unreadable []model.UnreadableScope `json:"unreadable_scopes"`
		Unmapped   []model.CoverageGap     `json:"unmapped_types"`
	} `json:"summary"`
}

// RenderInventory writes an inventory in any format.
func RenderInventory(w io.Writer, opts Options, rep InventoryReport) error {
	// Empty lists print as [], not null.
	rep.Resources = nonNil(rep.Resources)
	rep.Unreadable = nonNil(rep.Unreadable)
	rep.Unmapped = nonNil(rep.Unmapped)
	switch opts.Format {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case NDJSON:
		enc := json.NewEncoder(w)
		for _, r := range rep.Resources {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
		var t inventoryTrailer
		t.Summary.Resources = len(rep.Resources)
		t.Summary.Unreadable, t.Summary.Unmapped = rep.Unreadable, rep.Unmapped
		return enc.Encode(t)
	case CSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"cloud", "scope", "region", "type", "native_type", "id", "name", "created", "tags"})
		for _, r := range rep.Resources {
			created := ""
			if r.Created != nil {
				created = r.Created.Format(time.RFC3339)
			}
			_ = cw.Write([]string{string(r.Cloud), r.Scope, r.Region, r.Type, r.NativeType, r.ID, r.Name, created, tagList(r.Tags, "; ")})
		}
		cw.Flush()
		return cw.Error()
	case Markdown:
		var b strings.Builder
		b.WriteString("## tagherd inventory\n\n" + inventoryTotals(rep) + "\n")
		shown := rep.Resources[:min(len(rep.Resources), markdownFindingLimit)]
		if len(shown) > 0 {
			_, _ = fmt.Fprintf(&b, "\n### Resources (%d of %d)\n\n", len(shown), len(rep.Resources))
			b.WriteString(markdownTable([]string{"Cloud", "Scope", "Type", "Name", "Tags"}, inventoryRows(shown, false)))
		}
		_, err := io.WriteString(w, b.String())
		return err
	default:
		rows := Rows{Headers: []string{"CLOUD", "SCOPE", "REGION", "TYPE", "NAME", "TAGS"}, Rows: inventoryRows(rep.Resources, true)}
		for _, u := range rep.Unreadable {
			rows.Footer = append(rows.Footer, fmt.Sprintf("unreadable: %s %s: %s", u.Cloud, u.Scope, u.Error))
		}
		if len(rows.Footer) > 0 {
			rows.Footer = append(rows.Footer, "")
		}
		rows.Footer = append(rows.Footer, inventoryTotals(rep))
		return renderTable(w, rows, opts.Color)
	}
}

func inventoryRows(rs []model.Resource, region bool) [][]string {
	out := make([][]string, 0, len(rs))
	for _, r := range rs {
		typ := r.Type
		if typ == "" {
			typ = r.NativeType // discovered but matched no catalog entry
		}
		row := []string{string(r.Cloud), r.Scope}
		if region {
			row = append(row, r.Region)
		}
		out = append(out, append(row, typ, firstNonEmpty(r.Name, r.ID), tagList(r.Tags, ", ")))
	}
	return out
}

// tagList prints tags as key=value. GCP Resource Manager tags are marked,
// since they share key names with labels.
func tagList(tags []model.Tag, sep string) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = t.Key + "=" + t.Value
		if t.Kind == model.KindRMTag {
			parts[i] = "tag:" + parts[i]
		}
	}
	return strings.Join(parts, sep)
}

func inventoryTotals(rep InventoryReport) string {
	scopes := map[string]bool{}
	for _, r := range rep.Resources {
		scopes[string(r.Cloud)+"/"+r.Scope] = true
	}
	line := fmt.Sprintf("%d resource(s) in %d scope(s).", len(rep.Resources), len(scopes))
	if n := len(rep.Unreadable); n > 0 {
		line += fmt.Sprintf(" %d scope(s) unreadable.", n)
	}
	if n := len(rep.Unmapped); n > 0 {
		line += fmt.Sprintf(" %d coverage gap(s).", n)
	}
	return line
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
