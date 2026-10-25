package cli

import (
	"cmp"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/overad9658/tagherd/internal/engine"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/output"
	"github.com/overad9658/tagherd/internal/scan"
)

func (a *app) inventoryCmd() *cobra.Command {
	var fixture string
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "List discovered resources with their current tags",
		Long: `Discover resources in the selected scopes and list them with their current
tags, without evaluating them. Only the types the selected groups name are
discovered; --type narrows further. Read-only.

Exit codes: 0 success; 2 usage, config or policy error; 3 at least one scope
could not be read (the rest are still listed).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, err := a.loadPolicy()
			if err != nil {
				return err
			}
			groups, err := set.Select(a.g.groups)
			if err != nil {
				return output.Usagef("--group: %v", err)
			}
			providers, _, err := a.providers(cmd.Context(), fixture)
			if err != nil {
				return err
			}
			opts := a.scanOptions(nil)
			opts.Groups = groups
			in := scan.Discover(cmd.Context(), set, providers, opts)
			resources, warnings := engine.Classify(set.Catalog, in.Resources, a.g.types)
			for _, w := range warnings {
				a.log.Warn(w)
			}
			slices.SortStableFunc(resources, func(x, y model.Resource) int {
				return cmp.Or(cmp.Compare(x.Cloud, y.Cloud), cmp.Compare(x.Scope, y.Scope),
					cmp.Compare(x.Type, y.Type), cmp.Compare(x.Name, y.Name), cmp.Compare(x.ID, y.ID))
			})
			rep := output.InventoryReport{Resources: resources, Unreadable: in.Unreadable, Unmapped: in.Unmapped}
			if err := a.write(func(w io.Writer) error {
				return output.RenderInventory(w, output.Options{Format: a.format, Color: a.color}, rep)
			}); err != nil {
				return err
			}
			if n := len(in.Unreadable); n > 0 {
				return &output.ExitError{Code: output.ExitIncomplete, Err: fmt.Errorf("inventory incomplete: %d scope(s) could not be read", n)}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fixture, "fixture", "", "list a fake-provider YAML fixture instead of the clouds (testing)")
	_ = cmd.Flags().MarkHidden("fixture")
	return cmd
}
