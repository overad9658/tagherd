// Package cli is the cobra command layer. Commands resolve settings, call one
// component function and render its result; they hold no business logic.
package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/output"
	"github.com/overad9658/tagherd/internal/policy"
)

// EnvPrefix prefixes the environment variable for every flag: --policy-dir
// is TAGHERD_POLICY_DIR.
const EnvPrefix = "TAGHERD_"

const defaultPolicyDir = "policy"

// skipConfig marks commands that must work without a readable config file.
const skipConfig = "tagherd/skip-config"

type globals struct {
	configPath    string
	policyDir     string
	clouds        []string
	accounts      []string
	subscriptions []string
	projects      []string
	groups        []string
	types         []string
	output        string
	out           string
	concurrency   int
	profile       string
	verbose       bool
	quiet         bool
	noColor       bool
}

type app struct {
	g      globals
	stdout io.Writer
	stderr io.Writer
	getenv func(string) (string, bool)
	log    *slog.Logger
	cfg    *config.Config
	format output.Format
	color  bool
	clouds []model.Cloud
}

// Execute runs tagherd with args and returns the process exit code.
func Execute(args []string, stdout, stderr io.Writer, getenv func(string) (string, bool)) int {
	a := &app{stdout: stdout, stderr: stderr, getenv: getenv}
	root := a.rootCmd()
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		var ee *output.ExitError
		if !errors.As(err, &ee) || ee.Code == output.ExitUsage {
			fmt.Fprintf(stderr, "tagherd: %v\n", err)
		}
	}
	return output.ExitCode(err)
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "tagherd",
		Short: "Scan cloud resources against a tagging policy and fix their tags",
		Long: `tagherd scans AWS, Azure and GCP resources against a tagging policy, writes a
reviewable plan of tag changes, and applies it. scan reads, plan decides,
apply writes: nothing changes in a cloud account unless apply runs.`,
		SilenceErrors:     true,
		SilenceUsage:      true,
		PersistentPreRunE: a.setup,
	}
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fmt.Errorf("%w: %v", output.ErrUsage, err)
	})

	f := root.PersistentFlags()
	f.StringVar(&a.g.configPath, "config", "", "config file (default ./tagherd.yaml, then ~/.config/tagherd/tagherd.yaml)")
	f.StringVar(&a.g.policyDir, "policy-dir", "", "directory with tags.json and the group files (default ./policy)")
	f.StringSliceVar(&a.g.clouds, "cloud", nil, "limit to clouds: aws, azure, gcp")
	f.StringArrayVar(&a.g.accounts, "account", nil, "limit to AWS accounts; repeatable; globs allowed")
	f.StringArrayVar(&a.g.subscriptions, "subscription", nil, "limit to Azure subscriptions; repeatable; globs allowed")
	f.StringArrayVar(&a.g.projects, "project", nil, "limit to GCP projects; repeatable; globs allowed")
	f.StringArrayVar(&a.g.groups, "group", nil, "limit to policy groups, by name or file stem; repeatable; globs allowed")
	f.StringArrayVar(&a.g.types, "type", nil, "limit to resource types, by display name; repeatable; globs allowed")
	f.StringVarP(&a.g.output, "output", "o", "", "output format: table, json, ndjson, csv, markdown (default table on a terminal, json otherwise)")
	f.StringVar(&a.g.out, "out", "", "write output to a file instead of stdout")
	f.IntVar(&a.g.concurrency, "concurrency", 8, "number of scopes processed in parallel")
	f.StringVar(&a.g.profile, "profile", "", "AWS shared-config profile used as the base credential")
	f.BoolVarP(&a.g.verbose, "verbose", "v", false, "verbose logging")
	f.BoolVarP(&a.g.quiet, "quiet", "q", false, "only log warnings and errors")
	f.BoolVar(&a.g.noColor, "no-color", false, "disable color")

	root.AddCommand(
		a.initCmd(),
		a.policyCmd(),
		a.catalogCmd(),
		a.versionCmd(),
	)
	root.AddCommand(a.notYetCmds()...)
	return root
}

// setup applies environment variables, logging, config and output settings
// before any command runs.
func (a *app) setup(cmd *cobra.Command, _ []string) error {
	if err := a.applyEnv(cmd.Flags()); err != nil {
		return err
	}
	if a.g.verbose && a.g.quiet {
		return output.Usagef("-v and -q cannot be used together")
	}
	level := slog.LevelInfo
	switch {
	case a.g.verbose:
		level = slog.LevelDebug
	case a.g.quiet:
		level = slog.LevelWarn
	}
	a.log = slog.New(newLogHandler(a.stderr, level))

	for _, c := range a.g.clouds {
		cl, err := model.ParseCloud(strings.TrimSpace(c))
		if err != nil {
			return output.Usagef("--cloud: %v", err)
		}
		a.clouds = append(a.clouds, cl)
	}
	if a.g.concurrency < 1 {
		return output.Usagef("--concurrency must be at least 1")
	}

	toTerminal := a.g.out == "" && isTerminal(a.stdout)
	if a.g.output == "" {
		a.format = output.JSON
		if toTerminal {
			a.format = output.Table
		}
	} else {
		f, err := output.ParseFormat(a.g.output)
		if err != nil {
			return err
		}
		a.format = f
	}
	_, noColorEnv := a.getenv("NO_COLOR")
	a.color = toTerminal && !a.g.noColor && !noColorEnv

	if cmd.Annotations[skipConfig] == "true" {
		a.cfg = &config.Config{}
		return nil
	}
	path, err := config.Find(a.g.configPath)
	if err != nil {
		return err
	}
	if a.cfg, err = config.Load(path); err != nil {
		return err
	}
	if path != "" {
		a.log.Debug("loaded config", "path", path)
	}
	return nil
}

// applyEnv sets every flag not given on the command line from TAGHERD_<FLAG>.
func (a *app) applyEnv(fs *pflag.FlagSet) error {
	var err error
	fs.VisitAll(func(f *pflag.Flag) {
		if err != nil || f.Changed || f.Name == "help" {
			return
		}
		name := EnvPrefix + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		v, ok := a.getenv(name)
		if !ok {
			return
		}
		if setErr := fs.Set(f.Name, v); setErr != nil {
			err = output.Usagef("%s: %v", name, setErr)
		}
	})
	return err
}

// policyDir resolves the policy directory: flag or env, then config, then default.
func (a *app) policyDir() string {
	switch {
	case a.g.policyDir != "":
		return a.g.policyDir
	case a.cfg != nil && a.cfg.PolicyDir != "":
		return a.cfg.PolicyDir
	}
	return defaultPolicyDir
}

// loadPolicy loads the catalog and policy. Validation problems are logged to
// stderr; errors stop the command.
func (a *app) loadPolicy() (*policy.Set, error) {
	cat, err := catalog.Default()
	if err != nil {
		return nil, err
	}
	dir := a.policyDir()
	set, rep := policy.Load(dir, cat)
	for _, d := range rep.Diagnostics {
		if d.Level == policy.LevelError {
			a.log.Error(d.Message, "file", d.File, "path", d.Path)
		} else {
			a.log.Debug(d.Message, "level", string(d.Level), "file", d.File, "path", d.Path)
		}
	}
	if n := rep.Count(policy.LevelWarning); n > 0 && rep.Count(policy.LevelError) == 0 {
		a.log.Warn(fmt.Sprintf("policy has %d warning(s); run `tagherd policy validate` to see them", n))
	}
	if err := rep.Err(false); err != nil {
		return nil, fmt.Errorf("%s: %w (run `tagherd policy validate` for details)", dir, err)
	}
	return set, nil
}

// render writes a result to stdout or --out.
func (a *app) render(data any, rows output.Rows) error {
	w := a.stdout
	if a.g.out != "" {
		f, err := os.Create(a.g.out)
		if err != nil {
			return fmt.Errorf("--out: %w", err)
		}
		defer f.Close()
		w = f
	}
	return output.Render(w, output.Options{Format: a.format, Color: a.color}, data, rows)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// wantCloud reports whether --cloud allows c (no --cloud allows all).
func (a *app) wantCloud(c model.Cloud) bool {
	if len(a.clouds) == 0 {
		return true
	}
	for _, x := range a.clouds {
		if x == c {
			return true
		}
	}
	return false
}
