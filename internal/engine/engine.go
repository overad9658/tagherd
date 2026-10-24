// Package engine evaluates resources against the policy. It is a pure
// function of its inputs: no cloud SDKs, no clock, no I/O.
package engine

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
)

// OverrideKey names one group's override on one cloud.
type OverrideKey struct {
	Group string
	Cloud model.Cloud
}

// OverrideResult is what an override query returned: the provider IDs of the
// resources it reports as missing tags.
type OverrideResult struct {
	Missing map[string]bool
}

// Input is everything discovery produced.
type Input struct {
	Resources  []model.Resource
	Unreadable []model.UnreadableScope
	Unmapped   []model.CoverageGap
	// Overrides holds results for overrides that ran. A group with an
	// override that is absent here falls back to the default tag test.
	Overrides map[OverrideKey]*OverrideResult
}

// Options narrow the evaluation.
type Options struct {
	Groups []*policy.Group // selected groups; nil selects all
	Types  []string        // --type globs on the display name; none selects all
}

// Result is the scan's output.
type Result struct {
	Summary  model.Summary   `json:"summary"`
	Findings []model.Finding `json:"findings"`
}

// Evaluate classifies, matches and checks every resource.
func Evaluate(set *policy.Set, in Input, opts Options) Result {
	e := &evaluation{
		set:       set,
		selected:  map[*policy.Group]bool{},
		opts:      opts,
		in:        in,
		byGroup:   map[string]*tally{},
		byCloud:   map[string]*tally{},
		byScope:   map[scopeKey]*tally{},
		fallbacks: map[string]bool{},
		sum: model.Summary{
			ByTag:      map[string]int{},
			ByCode:     map[model.Code]int{},
			Unreadable: slices.Clone(in.Unreadable),
			Unmapped:   slices.Clone(in.Unmapped),
		},
	}
	groups := opts.Groups
	if groups == nil {
		groups = set.Groups
	}
	for _, g := range groups {
		e.selected[g] = true
	}

	for i := range in.Resources {
		e.resource(&in.Resources[i])
	}
	for _, u := range in.Unmapped {
		e.add(model.Finding{
			Cloud: u.Cloud, Scope: u.Scope, Type: u.Type,
			Group: e.groupNames(u.Cloud, u.Type), Code: model.UnmappedType,
		})
	}
	return e.finish()
}

type scopeKey struct {
	cloud model.Cloud
	scope string
}

type tally struct{ evaluated, compliant int }

func (t *tally) count(ok bool) {
	t.evaluated++
	if ok {
		t.compliant++
	}
}

type evaluation struct {
	set       *policy.Set
	selected  map[*policy.Group]bool
	opts      Options
	in        Input
	sum       model.Summary
	findings  []model.Finding
	overall   tally
	byGroup   map[string]*tally
	byCloud   map[string]*tally
	byScope   map[scopeKey]*tally
	fallbacks map[string]bool
}

func (e *evaluation) resource(r *model.Resource) {
	e.sum.Resources.Discovered++

	// 1. Classify.
	entry, err := e.set.Catalog.Classify(r.Cloud, r.NativeType, r.Attrs)
	if err != nil {
		if errors.Is(err, catalog.ErrAmbiguous) {
			e.sum.Warnings = append(e.sum.Warnings, fmt.Sprintf("%s: %v", r.ID, err))
		}
		e.sum.Resources.OutOfScope++
		return
	}
	if entry == nil || !MatchAny(e.opts.Types, entry.Display) {
		e.sum.Resources.OutOfScope++
		return
	}
	r.Type = entry.Display

	// 2. Match groups, then drop those whose name filter excludes it.
	var groups []*policy.Group
	inScope := false
	for _, g := range e.set.ByType[policy.TypeKey{Cloud: r.Cloud, Display: entry.Display}] {
		if !e.selected[g] {
			continue
		}
		inScope = true
		if f, ok := g.Filters[r.Cloud]; ok && f.Excludes(r.Name) {
			continue
		}
		groups = append(groups, g)
	}
	switch {
	case !inScope:
		e.sum.Resources.OutOfScope++
		return
	case len(groups) == 0:
		e.sum.Resources.Filtered++
		return
	}
	groupName := names(groups)
	for _, g := range groups {
		if g.Overrides[r.Cloud] != nil && e.in.Overrides[OverrideKey{g.Name, r.Cloud}] == nil {
			e.fallbacks[fmt.Sprintf("%s (%s)", g.Name, r.Cloud)] = true
		}
	}

	// Non-taggable types are listed but never evaluated.
	if entry.Taggable == catalog.NotTaggable {
		e.sum.Resources.NotTaggable++
		e.add(finding(r, groupName, result{code: model.NotTaggable}, ""))
		return
	}

	// 3. Evaluate each required tag of the union, in dictionary order.
	compliant := true
	for _, def := range e.required(groups) {
		res := checkTag(r.Cloud, def, r.Tags)
		if res.code == "" {
			continue
		}
		// 4. An override decides which resources count as missing tags.
		if (res.code == model.MissingTag || res.code == model.EmptyValue) && e.exempt(r, def, groups) {
			continue
		}
		compliant = false
		e.add(finding(r, groupName, res, def.Name))
	}

	e.sum.Resources.Evaluated++
	e.overall.count(compliant)
	for _, g := range groups {
		tallyFor(e.byGroup, g.Name).count(compliant)
	}
	tallyFor(e.byCloud, string(r.Cloud)).count(compliant)
	sk := scopeKey{r.Cloud, r.Scope}
	if e.byScope[sk] == nil {
		e.byScope[sk] = &tally{}
	}
	e.byScope[sk].count(compliant)
	if compliant {
		e.sum.Resources.Compliant++
	}
}

// required returns the union of the groups' tags in dictionary order.
func (e *evaluation) required(groups []*policy.Group) []*policy.TagDef {
	want := map[*policy.TagDef]bool{}
	for _, g := range groups {
		for _, t := range g.Tags {
			want[t] = true
		}
	}
	var out []*policy.TagDef
	for _, t := range e.set.Dict.Tags {
		if want[t] {
			out = append(out, t)
		}
	}
	return out
}

// exempt reports whether every group requiring the tag has an override on
// this cloud whose query did not report the resource. Groups whose override
// could not run fall back to the default test and never exempt.
func (e *evaluation) exempt(r *model.Resource, def *policy.TagDef, groups []*policy.Group) bool {
	covered := false
	for _, g := range groups {
		if !slices.Contains(g.Tags, def) {
			continue
		}
		res := e.in.Overrides[OverrideKey{g.Name, r.Cloud}]
		if g.Overrides[r.Cloud] == nil || res == nil || res.Missing[r.ID] {
			return false
		}
		covered = true
	}
	return covered
}

func (e *evaluation) add(f model.Finding) {
	e.findings = append(e.findings, f)
	e.sum.ByCode[f.Code]++
	if f.Tag != "" {
		e.sum.ByTag[f.Tag]++
	}
}

func (e *evaluation) groupNames(cloud model.Cloud, display string) string {
	var gs []*policy.Group
	for _, g := range e.set.ByType[policy.TypeKey{Cloud: cloud, Display: display}] {
		if e.selected[g] {
			gs = append(gs, g)
		}
	}
	return names(gs)
}

func (e *evaluation) finish() Result {
	c := &e.sum.Compliance
	c.Overall = model.NewRate("all", e.overall.evaluated, e.overall.compliant)
	for k, t := range e.byGroup {
		c.ByGroup = append(c.ByGroup, model.NewRate(k, t.evaluated, t.compliant))
	}
	for k, t := range e.byCloud {
		c.ByCloud = append(c.ByCloud, model.NewRate(k, t.evaluated, t.compliant))
	}
	for k, t := range e.byScope {
		r := model.NewRate(k.scope, t.evaluated, t.compliant)
		r.Cloud = k.cloud
		c.ByScope = append(c.ByScope, r)
	}
	model.SortRates(c.ByGroup)
	model.SortRates(c.ByCloud)
	model.SortRates(c.ByScope)
	for f := range e.fallbacks {
		e.sum.OverrideFallback = append(e.sum.OverrideFallback, f)
	}
	slices.Sort(e.sum.OverrideFallback)
	slices.Sort(e.sum.Warnings)
	slices.SortFunc(e.sum.Unreadable, func(a, b model.UnreadableScope) int {
		return strings.Compare(string(a.Cloud)+"/"+a.Scope, string(b.Cloud)+"/"+b.Scope)
	})
	slices.SortFunc(e.sum.Unmapped, func(a, b model.CoverageGap) int {
		return strings.Compare(string(a.Cloud)+"/"+a.Scope+"/"+a.Type, string(b.Cloud)+"/"+b.Scope+"/"+b.Type)
	})
	// Never null in JSON.
	if c.ByGroup == nil {
		c.ByGroup, c.ByCloud, c.ByScope = []model.Rate{}, []model.Rate{}, []model.Rate{}
	}
	if e.sum.Unreadable == nil {
		e.sum.Unreadable = []model.UnreadableScope{}
	}
	if e.sum.Unmapped == nil {
		e.sum.Unmapped = []model.CoverageGap{}
	}
	if e.findings == nil {
		e.findings = []model.Finding{}
	}
	model.SortFindings(e.findings)
	return Result{Summary: e.sum, Findings: e.findings}
}

func finding(r *model.Resource, group string, res result, tag string) model.Finding {
	return model.Finding{
		Cloud: r.Cloud, Scope: r.Scope, Region: r.Region, Group: group, Type: r.Type,
		ID: r.ID, Name: r.Name, Code: res.code, Tag: tag,
		Current: res.current, Proposed: res.proposed, Fixable: res.fixable,
	}
}

func names(groups []*policy.Group) string {
	ns := make([]string, len(groups))
	for i, g := range groups {
		ns[i] = g.Name
	}
	return strings.Join(ns, ", ")
}

func tallyFor(m map[string]*tally, k string) *tally {
	if m[k] == nil {
		m[k] = &tally{}
	}
	return m[k]
}

// MatchAny reports whether name matches any glob, ignoring case. No patterns
// matches everything. Used for --type and similar name filters.
func MatchAny(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	n := strings.ToLower(name)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if ok, err := path.Match(p, n); p == n || (err == nil && ok) {
			return true
		}
	}
	return false
}
