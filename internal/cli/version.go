package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/overad9658/tagherd/internal/output"
)

// Set at build time with -ldflags "-X github.com/overad9658/tagherd/internal/cli.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Print version information",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{skipConfig: "true"},
		RunE: func(*cobra.Command, []string) error {
			v := versionInfo{Version, Commit, Date, runtime.Version()}
			if a.format == output.Table {
				_, err := fmt.Fprintf(a.stdout, "tagherd %s (commit %s, built %s, %s)\n", v.Version, v.Commit, v.Date, v.Go)
				return err
			}
			rows := output.Rows{
				Headers: []string{"VERSION", "COMMIT", "DATE", "GO"},
				Rows:    [][]string{{v.Version, v.Commit, v.Date, v.Go}},
			}
			return a.render(v, rows)
		},
	}
}
