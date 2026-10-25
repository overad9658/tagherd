package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/overad9658/tagherd/internal/engine"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/output"
	"github.com/overad9658/tagherd/internal/policy"
	"github.com/overad9658/tagherd/internal/scan"
	"github.com/overad9658/tagherd/internal/values"
)

// defaultPlanFile is where plan writes without --out.
const defaultPlanFile = "plan.json"

func (a *app) planCmd() *cobra.Command {
	var (
		sets     []string
		defaults string
		grace    time.Duration
		fixture  string
	)
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Run a scan, then write a plan file of proposed tag changes",
		Long: `Run a scan, then write a plan of tag changes to --out (default plan.json) and
print it. Nothing is written to any cloud; apply does that.

Each fixable finding takes its value from the first source that has one:
--set, then defaults.yaml (type, group, scope, then cloud), then the parent's
tags (AWS account, Azure resource group, GCP project), then the built-in
rules (DeploymentType defaults to Manual, case fixes, ownership.yaml for
OwningOrg, synonyms.yaml for invalid values). Every value must pass the tag
dictionary. Findings with no source are listed as unresolved.

The route follows DeploymentType: Manual (or missing) is written directly,
Automation through Terraform, anything else not at all.

Exit codes: 0 plan written; 2 usage, config or policy error; 3 at least one
scope could not be read (the plan covers the rest).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if grace < 0 {
				return output.Usagef("--grace must not be negative")
			}
			set, err := a.loadPolicy()
			if err != nil {
				return err
			}
			groups, err := set.Select(a.g.groups)
			if err != nil {
				return output.Usagef("--group: %v", err)
			}
			src, err := a.planSources(set, sets, defaults)
			if err != nil {
				return err
			}
			providers, overrides, err := a.providers(cmd.Context(), fixture)
			if err != nil {
				return err
			}
			opts := a.scanOptions(overrides)
			opts.Groups = groups
			plan, _ := scan.Plan(cmd.Context(), set, providers, opts, scan.PlanOptions{Sources: src, Now: time.Now(), Grace: grace})

			path := a.g.out
			if path == "" {
				path = defaultPlanFile
			}
			if err := writePlan(path, plan); err != nil {
				return err
			}
			a.log.Info("wrote plan", "path", path, "id", plan.ID, "changes", plan.Summary.Changes, "unresolved", plan.Summary.Unresolved)
			if err := output.RenderPlan(a.stdout, output.Options{Format: a.format, Color: a.color}, plan); err != nil {
				return err
			}
			if n := len(plan.Summary.Unreadable); n > 0 {
				return &output.ExitError{Code: output.ExitIncomplete, Err: fmt.Errorf("plan incomplete: %d scope(s) could not be read", n)}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&sets, "set", nil, "supply a value for a tag, as Tag=Value, for every resource in the run; repeatable")
	cmd.Flags().StringVar(&defaults, "defaults", "", "defaults file of tag values by cloud, scope, group or type (default: defaults_file in tagherd.yaml)")
	cmd.Flags().DurationVar(&grace, "grace", 5*time.Minute, "skip resources created more recently than this; 0 skips none")
	cmd.Flags().StringVar(&fixture, "fixture", "", "plan from a fake-provider YAML fixture instead of the clouds (testing)")
	_ = cmd.Flags().MarkHidden("fixture")
	return cmd
}

// planSources loads --set and the value files named by the flags and config.
func (a *app) planSources(set *policy.Set, sets []string, defaultsFlag string) (engine.Sources, error) {
	var src engine.Sources
	var err error
	if src.Set, err = values.ParseSet(sets, set.Dict); err != nil {
		return src, output.Usagef("%v", err)
	}
	defaultsFile := a.cfg.DefaultsFile
	if defaultsFlag != "" {
		defaultsFile = defaultsFlag
	}
	if src.Defaults, err = values.LoadDefaults(defaultsFile, set); err != nil {
		return src, fmt.Errorf("defaults: %w", err)
	}
	if src.Ownership, err = values.LoadOwnership(a.cfg.OwnershipFile, set.Dict); err != nil {
		return src, fmt.Errorf("ownership_file: %w", err)
	}
	if src.Synonyms, err = values.LoadSynonyms(a.cfg.SynonymsFile, set.Dict); err != nil {
		return src, fmt.Errorf("synonyms_file: %w", err)
	}
	return src, nil
}

// writePlan writes the plan as indented JSON, readable only by its owner.
// It goes to a temporary file first, so a failed write never leaves half a
// plan behind.
func writePlan(path string, p model.Plan) (err error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tagherd-plan-*")
	if err != nil {
		return fmt.Errorf("--out: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("--out: %w", err)
	}
	if err = f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("--out: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("--out: %w", err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("--out: %w", err)
	}
	return nil
}
