package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/engine"
	"github.com/overad9658/tagherd/internal/output"
)

func (a *app) catalogCmd() *cobra.Command {
	var gaps bool
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "List known resource types and whether each is taggable",
		Long: `List the type catalog: each display name the policy may use, its provider
resource type, how tagherd discovers it, and whether tagherd can write tags
to it. Filter with --cloud and --type (case-insensitive globs on the display
name).

Discovery is bulk (AWS Config, Azure Resource Graph or Cloud Asset
Inventory), list:<adapter> (a per-service list call) or none. --gaps lists
only the types with no discovery adapter, which scans report as
UNMAPPED_TYPE.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cat, err := catalog.Default()
			if err != nil {
				return err
			}
			var data []*catalog.Entry
			rows := output.Rows{Headers: []string{"CLOUD", "TYPE", "NATIVE TYPE", "DISCOVERY", "TAGGABLE", "NOTES"}}
			for _, e := range cat.Entries {
				if !a.wantCloud(e.Cloud) || !engine.MatchAny(a.g.types, e.Display) {
					continue
				}
				canonical := cat.Canonical(e)
				mode, adapter := canonical.DiscoveryMode()
				if gaps && mode != catalog.DiscoverNone {
					continue
				}
				data = append(data, e)
				native := strings.Join(e.NativeTypes, ", ")
				if e.AliasOf != "" {
					native = "= " + e.AliasOf
				}
				discovery := mode
				if adapter != "" {
					discovery += ":" + adapter
				}
				rows.Rows = append(rows.Rows, []string{string(e.Cloud), e.Display, native, discovery, string(e.Taggable), notes(e)})
			}
			if gaps {
				rows.Footer = []string{fmt.Sprintf("%d type(s) without a discovery adapter.", len(rows.Rows))}
			}
			return a.render(data, rows)
		},
	}
	cmd.Flags().BoolVar(&gaps, "gaps", false, "list only types with no discovery adapter (reported as UNMAPPED_TYPE)")
	return cmd
}

func notes(e *catalog.Entry) string {
	var parts []string
	if len(e.Match) > 0 {
		var m []string
		for k := range e.Match {
			m = append(m, k)
		}
		parts = append(parts, "match: "+strings.Join(sorted(m), ", "))
	}
	if e.WriteScope != "" {
		parts = append(parts, "write: "+e.WriteScope)
	}
	if e.Note != "" {
		parts = append(parts, e.Note)
	}
	return strings.Join(parts, "; ")
}

func sorted(xs []string) []string {
	sort.Strings(xs)
	return xs
}
