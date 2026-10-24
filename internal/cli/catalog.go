package cli

import (
	"path"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/output"
)

func (a *app) catalogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "catalog",
		Short: "List known resource types and whether each is taggable",
		Long: `List the type catalog: each display name the policy may use, its provider
resource type, and whether tagherd can write tags to it. Filter with --cloud
and --type (case-insensitive globs on the display name).`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cat, err := catalog.Default()
			if err != nil {
				return err
			}
			var data []*catalog.Entry
			rows := output.Rows{Headers: []string{"CLOUD", "TYPE", "NATIVE TYPE", "TAGGABLE", "NOTES"}}
			for _, e := range cat.Entries {
				if !a.wantCloud(e.Cloud) || !matchAny(a.g.types, e.Display) {
					continue
				}
				data = append(data, e)
				native := strings.Join(e.NativeTypes, ", ")
				if e.AliasOf != "" {
					native = "= " + e.AliasOf
				}
				rows.Rows = append(rows.Rows, []string{string(e.Cloud), e.Display, native, string(e.Taggable), notes(e)})
			}
			return a.render(data, rows)
		},
	}
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

// matchAny reports whether name matches any pattern, ignoring case. No
// patterns matches everything.
func matchAny(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	n := strings.ToLower(name)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == n {
			return true
		}
		if ok, err := path.Match(p, n); err == nil && ok {
			return true
		}
	}
	return false
}

func sorted(xs []string) []string {
	sort.Strings(xs)
	return xs
}
