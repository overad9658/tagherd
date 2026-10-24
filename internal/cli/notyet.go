package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/overad9658/tagherd/internal/output"
)

// notYetCmds registers the commands from later phases so they appear in help
// and fail clearly instead of as unknown commands.
func (a *app) notYetCmds() []*cobra.Command {
	pending := []struct{ use, short, phase string }{
		{"inventory", "List discovered resources with their current tags", "phase 3"},
		{"plan", "Run a scan and write a plan file of proposed tag changes", "phase 4"},
		{"apply", "Execute a plan: direct tag writes, or Terraform changes", "phase 5"},
	}
	var cmds []*cobra.Command
	for _, p := range pending {
		cmds = append(cmds, &cobra.Command{
			Use:                p.use,
			Short:              p.short + " (not yet implemented)",
			DisableFlagParsing: true,
			Annotations:        map[string]string{skipConfig: "true"},
			RunE: func(*cobra.Command, []string) error {
				return &output.ExitError{
					Code: output.ExitUsage,
					Err:  fmt.Errorf("%s is not implemented yet; it arrives in %s", p.use, p.phase),
				}
			},
		})
	}
	return cmds
}
