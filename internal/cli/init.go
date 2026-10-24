package cli

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed templates
var templates embed.FS

func (a *app) initCmd() *cobra.Command {
	var dir string
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create tagherd.yaml and a policy directory scaffold",
		Long: `Create tagherd.yaml, ownership.yaml, defaults.yaml and synonyms.yaml in --dir,
plus an example policy directory when ./policy does not exist yet. Existing
files are kept unless --force is set; an existing policy directory is never
touched.`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{skipConfig: "true"},
		RunE: func(*cobra.Command, []string) error {
			_, statErr := os.Stat(filepath.Join(dir, defaultPolicyDir))
			policyExists := statErr == nil
			return fs.WalkDir(templates, "templates", func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel := strings.TrimPrefix(p, "templates/")
				dst := filepath.Join(dir, filepath.FromSlash(rel))
				inPolicy := path.Dir(rel) == defaultPolicyDir
				switch _, err := os.Stat(dst); {
				case inPolicy && policyExists:
					return nil
				case err == nil && !force:
					_, err := fmt.Fprintf(a.stdout, "kept     %s (exists; --force to overwrite)\n", dst)
					return err
				case err != nil && !errors.Is(err, fs.ErrNotExist):
					return err
				}
				data, err := templates.ReadFile(p)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(dst, data, 0o644); err != nil {
					return err
				}
				_, err = fmt.Fprintf(a.stdout, "created  %s\n", dst)
				return err
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory to create the files in")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing config files (never the policy directory)")
	return cmd
}
