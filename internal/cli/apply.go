package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/overad9658/tagherd/internal/apply"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/output"
	"github.com/overad9658/tagherd/internal/provider"
)

// defaultAuditLog is where every attempted write is recorded.
const defaultAuditLog = "tagherd-audit.ndjson"

func (a *app) applyCmd() *cobra.Command {
	var (
		planFile   string
		yes        bool
		only       string
		pr         bool
		verify     bool
		allowStale bool
		auditLog   string
		fixture    string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Execute a plan: direct tag writes, or Terraform changes",
		Long: `Execute a plan file made by tagherd plan. Direct changes (Manual resources) are
written through each cloud's tagging API; Terraform changes arrive in phase 6
and are listed as not applied.

Before writing, apply checks that the policy has not changed since the plan
(exit 2 otherwise) and that the plan is under 24 hours old (or
--allow-stale). It prints a summary and asks for confirmation unless --yes is
set; without a terminal, --yes is required.

Each change is checked against the tag found just before the write: a tag
that changed since the plan is skipped as stale, and one already holding the
new value is left alone, so applying a plan twice changes nothing. Tags are
never removed. Every attempt is appended to the audit log.

Exit codes: 0 every change applied or already set; 2 usage, config or policy
error, a plan that fails the checks, or a cancelled confirmation; 4 at least
one change failed or was stale.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case planFile == "":
				return output.Usagef("--plan is required")
			case only != "" && only != "direct" && only != "terraform":
				return output.Usagef("--only must be direct or terraform, got %q", only)
			case only == "terraform" || pr:
				return output.Usagef("the Terraform route arrives in phase 6; use --only direct")
			}
			plan, err := readPlan(planFile)
			if err != nil {
				return err
			}
			set, err := a.loadPolicy()
			if err != nil {
				return err
			}
			if err := apply.Check(plan, set.Hash, time.Now(), allowStale); err != nil {
				return output.Usagef("%s: %v", planFile, err)
			}
			plan.Changes = a.applicable(plan.Changes, only)

			direct := 0
			for _, c := range plan.Changes {
				if c.Route == model.RouteDirect {
					direct++
				}
			}
			a.confirmSummary(plan)
			if direct > 0 && !yes {
				if err := a.confirm(direct); err != nil {
					return err
				}
			}

			// Only the clouds with direct changes need credentials.
			if len(a.clouds) == 0 {
				for _, c := range model.Clouds {
					for _, ch := range plan.Changes {
						if ch.Cloud == c && ch.Route == model.RouteDirect {
							a.clouds = append(a.clouds, c)
							break
						}
					}
				}
			}
			var ps map[model.Cloud]provider.Provider
			if direct > 0 {
				if ps, _, err = a.providers(cmd.Context(), fixture); err != nil {
					return err
				}
			}
			audit, err := os.OpenFile(auditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return fmt.Errorf("--audit-log: %w", err)
			}
			defer func() { _ = audit.Close() }()

			opts := apply.Options{Providers: ps, Concurrency: a.g.concurrency, Audit: audit, Log: a.log}
			rep := apply.Run(cmd.Context(), plan, opts)
			if verify {
				findings, warnings := apply.Verify(cmd.Context(), set, rep, opts)
				for _, w := range warnings {
					a.log.Warn(w)
				}
				rep.Verify = findings
				if rep.Verify == nil {
					rep.Verify = []model.Finding{}
				}
			}
			a.log.Info("applied plan", "id", plan.ID, "written", rep.Summary.Written, "audit_log", auditLog)
			if err := a.write(func(w io.Writer) error {
				return output.RenderApply(w, output.Options{Format: a.format, Color: a.color}, rep)
			}); err != nil {
				return err
			}
			if n := rep.Summary.Failed + rep.Summary.Stale; n > 0 {
				return &output.ExitError{Code: output.ExitApplyFailed, Err: fmt.Errorf("%d change(s) failed or were stale", n)}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&planFile, "plan", "", "the plan file to execute (required)")
	f.BoolVar(&yes, "yes", false, "skip the confirmation prompt (required without a terminal)")
	f.StringVar(&only, "only", "", "execute one write path only: direct or terraform")
	f.BoolVar(&pr, "pr", false, "open pull requests for Terraform changes (phase 6)")
	f.BoolVar(&verify, "verify", false, "re-read changed resources afterwards and report any still non-compliant")
	f.BoolVar(&allowStale, "allow-stale", false, "accept a plan more than 24 hours old (never a policy change)")
	f.StringVar(&auditLog, "audit-log", defaultAuditLog, "file every attempted write is appended to")
	f.StringVar(&fixture, "fixture", "", "apply to a fake-provider YAML fixture instead of the clouds (testing)")
	_ = f.MarkHidden("fixture")
	return cmd
}

func readPlan(path string) (model.Plan, error) {
	var p model.Plan
	data, err := os.ReadFile(path)
	if err != nil {
		return p, output.Usagef("--plan: %v", err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, output.Usagef("--plan: %s is not a plan file: %v", path, err)
	}
	if p.ID == "" || p.PolicyHash == "" {
		return p, output.Usagef("--plan: %s is not a plan file (no id or policy_hash)", path)
	}
	return p, nil
}

// applicable keeps the changes --cloud and --only select.
func (a *app) applicable(cs []model.Change, only string) []model.Change {
	var out []model.Change
	for _, c := range cs {
		if !a.wantCloud(c.Cloud) || (only == "direct" && c.Route != model.RouteDirect) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// confirmSummary prints changes by route and cloud to stderr.
func (a *app) confirmSummary(p model.Plan) {
	type key struct {
		route model.Route
		cloud model.Cloud
	}
	counts := map[key]int{}
	resources := map[key]map[string]bool{}
	for _, c := range p.Changes {
		k := key{c.Route, c.Cloud}
		counts[k]++
		if resources[k] == nil {
			resources[k] = map[string]bool{}
		}
		resources[k][c.ResourceID] = true
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].cloud < keys[j].cloud
	})
	rows := output.Rows{Headers: []string{"ROUTE", "CLOUD", "CHANGES", "RESOURCES"}}
	for _, k := range keys {
		rows.Rows = append(rows.Rows, []string{string(k.route), string(k.cloud), fmt.Sprint(counts[k]), fmt.Sprint(len(resources[k]))})
	}
	_, _ = fmt.Fprintf(a.stderr, "Plan %s, made %s:\n\n", p.ID, p.Created.Local().Format(time.RFC1123))
	_ = output.Render(a.stderr, output.Options{Format: output.Table}, nil, rows)
	_, _ = fmt.Fprintln(a.stderr)
}

// confirm asks for "yes" on a terminal.
func (a *app) confirm(direct int) error {
	if !isTerminal(a.stdin) {
		return output.Usagef("stdin is not a terminal: pass --yes to apply without confirmation")
	}
	_, _ = fmt.Fprintf(a.stderr, "Write %d direct change(s)? Type yes to continue: ", direct)
	line, _ := bufio.NewReader(a.stdin).ReadString('\n')
	if strings.TrimSpace(line) != "yes" {
		return output.Usagef("cancelled; nothing was written")
	}
	return nil
}
