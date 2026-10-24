package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/output"
	"github.com/overad9658/tagherd/internal/provider"
	"github.com/overad9658/tagherd/internal/provider/fake"
	"github.com/overad9658/tagherd/internal/scan"
)

// failOn maps --fail-on values to the finding classes that set exit code 1.
var failOn = map[string][]model.Class{
	"any":     {model.ClassMissing, model.ClassInvalid},
	"missing": {model.ClassMissing},
	"invalid": {model.ClassInvalid},
	"none":    nil,
}

func (a *app) scanCmd() *cobra.Command {
	var (
		fail        string
		summaryOnly bool
		fixture     string
	)
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Discover resources, evaluate them and report findings",
		Long: `Discover resources in the selected scopes, evaluate them against the policy
and report findings. Read-only and deterministic.

Exit codes: 0 no findings in the --fail-on classes; 1 findings in them;
2 usage, config or policy error; 3 at least one scope could not be read
(results for the rest are still printed). The highest applicable code wins.

--fail-on classes: missing = MISSING_TAG, EMPTY_VALUE; invalid =
INVALID_VALUE, CASE_MISMATCH, DUPLICATE_KEY, INVALID_DEPLOYMENT_TYPE;
any = both; none = never exit 1.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			classes, ok := failOn[fail]
			if !ok {
				return output.Usagef("--fail-on must be any, missing, invalid or none, got %q", fail)
			}
			set, err := a.loadPolicy()
			if err != nil {
				return err
			}
			groups, err := set.Select(a.g.groups)
			if err != nil {
				return output.Usagef("--group: %v", err)
			}
			providers, overrides, err := a.providers(fixture)
			if err != nil {
				return err
			}
			res := scan.Run(cmd.Context(), set, providers, scan.Options{
				Clouds: a.clouds,
				ScopePatterns: map[model.Cloud][]string{
					model.AWS: a.g.accounts, model.Azure: a.g.subscriptions, model.GCP: a.g.projects,
				},
				Groups:      groups,
				Types:       a.g.types,
				Concurrency: a.g.concurrency,
				Overrides:   overrides,
				Log:         a.log,
			})
			for _, w := range res.Summary.Warnings {
				a.log.Warn(w)
			}

			rep := output.ScanReport{Summary: res.Summary, Findings: res.Findings}
			if err := a.write(func(w io.Writer) error {
				return output.RenderScan(w, output.Options{Format: a.format, Color: a.color}, rep, summaryOnly)
			}); err != nil {
				return err
			}
			return scanExit(res.Findings, res.Summary, classes)
		},
	}
	cmd.Flags().StringVar(&fail, "fail-on", "any", "what makes the exit code 1: any, missing, invalid or none")
	cmd.Flags().BoolVar(&summaryOnly, "summary", false, "show compliance percentages only, by group, cloud and scope")
	cmd.Flags().StringVar(&fixture, "fixture", "", "scan a fake-provider YAML fixture instead of the clouds (testing)")
	_ = cmd.Flags().MarkHidden("fixture")
	return cmd
}

// providers returns the discovery adapters. Until the cloud adapters land in
// phase 3, only the fake provider exists.
func (a *app) providers(fixture string) (map[model.Cloud]provider.Provider, scan.OverrideRunner, error) {
	if fixture == "" {
		return nil, nil, &output.ExitError{
			Code: output.ExitUsage,
			Err:  errors.New("cloud adapters arrive in phase 3; scan a fake-provider fixture with --fixture <file>"),
		}
	}
	env, err := fake.Load(fixture)
	if err != nil {
		return nil, nil, output.Usagef("--fixture: %v", err)
	}
	out := map[model.Cloud]provider.Provider{}
	for c, p := range env.Providers {
		out[c] = p
	}
	if env.Wiz == nil {
		return out, nil, nil
	}
	return out, env.Wiz, nil
}

// scanExit returns the exit code for a finished scan: 3 for unreadable
// scopes, else 1 for findings in the --fail-on classes, else success.
func scanExit(findings []model.Finding, sum model.Summary, classes []model.Class) error {
	if n := len(sum.Unreadable); n > 0 {
		return &output.ExitError{Code: output.ExitIncomplete, Err: fmt.Errorf("scan incomplete: %d scope(s) could not be read", n)}
	}
	n := 0
	for _, f := range findings {
		for _, c := range classes {
			if f.Code.Class() == c {
				n++
			}
		}
	}
	if n > 0 {
		return &output.ExitError{Code: output.ExitFindings, Err: fmt.Errorf("%d finding(s) in the --fail-on classes", n)}
	}
	return nil
}
