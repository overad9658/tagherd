package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/output"
	"github.com/overad9658/tagherd/internal/policy"
)

func (a *app) policyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Validate and inspect the tagging policy",
	}
	cmd.AddCommand(a.policyValidateCmd(), a.policyListCmd(), a.policyShowCmd())
	return cmd
}

type validateResult struct {
	Valid       bool                `json:"valid"`
	PolicyDir   string              `json:"policy_dir"`
	Hash        string              `json:"hash,omitempty"`
	Groups      int                 `json:"groups"`
	Types       map[model.Cloud]int `json:"types"`
	Errors      int                 `json:"errors"`
	Warnings    int                 `json:"warnings"`
	Diagnostics []policy.Diagnostic `json:"diagnostics"`
}

func (a *app) policyValidateCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check schema, dictionary references, catalog names, overlaps and override syntax",
		Long: `Check the policy directory: the group file schema, tags against tags.json,
display names against the type catalog, types listed in several groups, and
override query syntax. Exits 2 on any error, or on any warning with --strict.
Info messages are listed in table output with -v.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cat, err := catalog.Default()
			if err != nil {
				return err
			}
			dir := a.policyDir()
			set, rep := policy.Load(dir, cat)
			res := validateResult{
				PolicyDir:   dir,
				Types:       map[model.Cloud]int{},
				Errors:      rep.Count(policy.LevelError),
				Warnings:    rep.Count(policy.LevelWarning),
				Diagnostics: rep.Diagnostics,
			}
			if set != nil {
				res.Hash = set.Hash
				res.Groups = len(set.Groups)
				for _, g := range set.Groups {
					for c, ts := range g.Targets {
						res.Types[c] += len(ts)
					}
				}
			}
			verr := rep.Err(strict)
			res.Valid = verr == nil

			rows := output.Rows{Headers: []string{"LEVEL", "FILE", "PATH", "MESSAGE"}}
			for _, d := range rep.Diagnostics {
				if d.Level == policy.LevelInfo && !a.g.verbose && a.format == output.Table {
					continue
				}
				rows.Rows = append(rows.Rows, []string{string(d.Level), d.File, d.Path, d.Message})
			}
			rows.Footer = []string{validateSummary(res, rep.Count(policy.LevelInfo))}
			if len(rows.Rows) == 0 {
				rows.Headers = nil
			}
			if err := a.render(res, rows); err != nil {
				return err
			}
			if verr != nil {
				return &output.ExitError{Code: output.ExitUsage, Err: verr}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors")
	return cmd
}

func validateSummary(r validateResult, infos int) string {
	status := "valid"
	if !r.Valid {
		status = "INVALID"
	}
	s := fmt.Sprintf("Policy %s: %d groups, %d AWS / %d Azure / %d GCP / %d OCI types; %d errors, %d warnings, %d info.",
		status, r.Groups, r.Types[model.AWS], r.Types[model.Azure], r.Types[model.GCP], r.Types[model.OCI], r.Errors, r.Warnings, infos)
	if r.Hash != "" {
		s += " Hash " + r.Hash[:12] + "."
	}
	return s
}

type groupSummary struct {
	Name        string              `json:"name"`
	File        string              `json:"file"`
	Description string              `json:"description,omitempty"`
	Tags        []string            `json:"tags"`
	Types       map[model.Cloud]int `json:"types"`
	Filters     bool                `json:"filters"`
	Override    bool                `json:"override"`
}

func (a *app) policyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List policy groups",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			set, err := a.loadPolicy()
			if err != nil {
				return err
			}
			groups, err := set.Select(a.g.groups)
			if err != nil {
				return output.Usagef("--group: %v", err)
			}
			var data []groupSummary
			rows := output.Rows{Headers: []string{"GROUP", "FILE", "TAGS", "AWS", "AZURE", "GCP", "OCI"}}
			for _, g := range groups {
				s := groupSummary{
					Name: g.Name, File: g.File, Description: g.Description, Tags: g.TagNames,
					Types: map[model.Cloud]int{}, Filters: len(g.Filters) > 0, Override: len(g.Overrides) > 0,
				}
				row := []string{g.Name, g.File, strconv.Itoa(len(g.TagNames))}
				for _, c := range model.Clouds {
					s.Types[c] = len(g.Targets[c])
					row = append(row, strconv.Itoa(len(g.Targets[c])))
				}
				data = append(data, s)
				rows.Rows = append(rows.Rows, row)
			}
			return a.render(data, rows)
		},
	}
}

func (a *app) policyShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <group>",
		Short: "Print one group with its resolved tags and types",
		Long: `Print a group's required tags, filters, override and resource types, with
each type's provider type and taggability from the catalog. <group> is a
group name or file stem, ignoring case; globs match several groups.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			set, err := a.loadPolicy()
			if err != nil {
				return err
			}
			groups, err := set.Select(args)
			if err != nil {
				return output.Usagef("%v (see `tagherd policy list`)", err)
			}
			rows := output.Rows{Headers: []string{"GROUP", "CLOUD", "TYPE", "NATIVE TYPE", "TAGGABLE"}}
			for _, g := range groups {
				for _, c := range model.Clouds {
					if !a.wantCloud(c) {
						continue
					}
					for _, t := range g.Targets[c] {
						display := t.Display
						if !strings.EqualFold(display, t.Entry.Display) {
							display += " (= " + t.Entry.Display + ")"
						}
						rows.Rows = append(rows.Rows, []string{
							g.Name, string(c), display, strings.Join(t.Entry.NativeTypes, ", "), string(t.Entry.Taggable),
						})
					}
				}
				rows.Footer = append(rows.Footer, showFooter(g)...)
			}
			if len(groups) == 1 {
				return a.render(groups[0], rows)
			}
			return a.render(groups, rows)
		},
	}
}

func showFooter(g *policy.Group) []string {
	lines := []string{fmt.Sprintf("%s (%s)", g.Name, g.File)}
	if g.Description != "" {
		lines = append(lines, "  "+g.Description)
	}
	lines = append(lines, "  Required tags: "+strings.Join(g.TagNames, ", "))
	for _, c := range model.Clouds {
		if f, ok := g.Filters[c]; ok {
			lines = append(lines, fmt.Sprintf("  %s filter: name does not contain %s", c, strings.Join(f.DoesNotContain, ", ")))
		}
		if _, ok := g.Overrides[c]; ok {
			lines = append(lines, fmt.Sprintf("  %s override: missing-tag test replaced by a Wiz graph query", c))
		}
	}
	return lines
}
