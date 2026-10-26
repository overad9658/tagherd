package engine

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
	"github.com/overad9658/tagherd/internal/values"
)

// ParentKey names a resource's parent: an AWS account, an Azure resource
// group or a GCP project.
type ParentKey struct {
	Cloud         model.Cloud
	Scope, Parent string
}

// ParentOf returns the key a resource's parent tags are cached under.
func ParentOf(r model.Resource) ParentKey {
	return ParentKey{Cloud: r.Cloud, Scope: r.Scope, Parent: r.Parent}
}

// Sources are the values a plan may use, already checked against the
// dictionary. Nil maps and files supply nothing.
type Sources struct {
	Set       map[string]string // --set, by dictionary tag name
	Defaults  *values.Defaults
	Ownership *values.Ownership
	Synonyms  *values.Synonyms
	Parents   map[ParentKey][]model.Tag
}

// PlanOptions control BuildPlan.
type PlanOptions struct {
	Now   time.Time
	Grace time.Duration // resources created after Now-Grace are skipped
}

// Reasons a finding stays unresolved.
const (
	ReasonNoSource        = "no value source"
	ReasonNoSynonym       = "no synonym for this value"
	ReasonNotFixable      = "not fixable automatically"
	ReasonBadDeployment   = "DeploymentType is invalid, so nothing is written to this resource"
	ReasonLimit           = "no value source fits the cloud's tag limits"
	reasonDuplicateDeploy = "DeploymentType keys conflict, so nothing is written to this resource"
)

// gcpWriteGroups are the group-name prefixes GCP writes are limited to.
var gcpWriteGroups = []string{"Compute", "Database", "Storage"}

// ParentRequests returns, for each parent whose tags the plan may need,
// one resource to ask the provider with. Only missing or empty tags other
// than DeploymentType can be inherited.
func ParentRequests(in Input, res Result) map[ParentKey]model.Resource {
	byID := resourcesByID(in.Resources)
	out := map[ParentKey]model.Resource{}
	for _, f := range res.Findings {
		if (f.Code != model.MissingTag && f.Code != model.EmptyValue) || f.Tag == tagDeploymentType {
			continue
		}
		if r, ok := byID[resourceKey{f.Cloud, f.ID}]; ok {
			if k := ParentOf(*r); out[k].ID == "" {
				out[k] = *r
			}
		}
	}
	return out
}

type resourceKey struct {
	cloud model.Cloud
	id    string
}

func resourcesByID(rs []model.Resource) map[resourceKey]*model.Resource {
	out := make(map[resourceKey]*model.Resource, len(rs))
	for i := range rs {
		k := resourceKey{rs[i].Cloud, rs[i].ID}
		if out[k] == nil {
			out[k] = &rs[i]
		}
	}
	return out
}

// BuildPlan turns a scan's findings into tag changes. Each finding either
// becomes a change, with the rule that produced its value, or is listed as
// unresolved with a reason; nothing is guessed. The caller sets the plan's
// ID and creation time.
func BuildPlan(set *policy.Set, in Input, res Result, src Sources, opts PlanOptions) model.Plan {
	b := &planner{set: set, src: src, opts: opts}
	byID := resourcesByID(in.Resources)

	// Findings are sorted by cloud, scope, type and ID, so each resource's
	// findings are adjacent.
	var batch []model.Finding
	flush := func() {
		if len(batch) > 0 {
			if r := byID[resourceKey{batch[0].Cloud, batch[0].ID}]; r != nil {
				b.resource(r, batch)
			}
		}
		batch = batch[:0]
	}
	for _, f := range res.Findings {
		if f.Code == model.NotTaggable || f.Code == model.UnmappedType {
			continue
		}
		if len(batch) > 0 && (batch[0].Cloud != f.Cloud || batch[0].ID != f.ID) {
			flush()
		}
		batch = append(batch, f)
	}
	flush()

	model.SortChanges(b.changes)
	unresolved := b.unresolved
	slices.SortStableFunc(unresolved, func(x, y model.Unresolved) int {
		return cmp.Or(cmp.Compare(x.Cloud, y.Cloud), cmp.Compare(x.Scope, y.Scope), cmp.Compare(x.Type, y.Type),
			cmp.Compare(x.ID, y.ID), cmp.Compare(x.Tag, y.Tag), cmp.Compare(x.Code, y.Code))
	})
	sum := model.PlanSummary{
		ByRoute:      map[model.Route]int{},
		ByCloud:      map[model.Cloud]int{},
		Unresolved:   len(unresolved),
		SkippedGrace: b.skipped,
		Unreadable:   slices.Clone(in.Unreadable),
	}
	resources := map[resourceKey]bool{}
	for _, c := range b.changes {
		sum.Changes++
		sum.ByRoute[c.Route]++
		sum.ByCloud[c.Cloud]++
		resources[resourceKey{c.Cloud, c.ResourceID}] = true
	}
	sum.Resources = len(resources)
	if sum.Unreadable == nil {
		sum.Unreadable = []model.UnreadableScope{}
	}
	scopes := slices.Clone(in.Scopes)
	slices.Sort(scopes)
	return model.Plan{
		PolicyHash: set.Hash,
		Grace:      opts.Grace.String(),
		Scopes:     nonNil(slices.Compact(scopes)),
		Summary:    sum,
		Changes:    nonNil(b.changes),
		Unresolved: nonNil(unresolved),
	}
}

type planner struct {
	set        *policy.Set
	src        Sources
	opts       PlanOptions
	changes    []model.Change
	unresolved []model.Unresolved
	skipped    int
}

// resource plans one resource's findings.
func (b *planner) resource(r *model.Resource, fs []model.Finding) {
	if r.Created != nil && b.opts.Grace > 0 && r.Created.After(b.opts.Now.Add(-b.opts.Grace)) {
		b.skipped++ // pipelines get time to tag it first
		return
	}
	// The effective DeploymentType picks the route. An invalid or
	// conflicting one stops the resource; a missing one gets a value
	// through the chain like any other tag.
	for _, f := range fs {
		if f.Tag == tagDeploymentType && (f.Code == model.InvalidDeploymentType || f.Code == model.DuplicateKey) {
			reason := ReasonBadDeployment
			if f.Code == model.DuplicateKey {
				reason = reasonDuplicateDeploy
			}
			for _, g := range fs {
				why := reason
				if g.Tag == tagDeploymentType {
					why = ReasonNotFixable
				}
				b.unresolved = append(b.unresolved, model.Unresolved{Finding: g, Reason: why})
			}
			return
		}
	}
	deployment := currentDeployment(b.set.Dict, r)
	var planned []model.Change
	for _, f := range fs {
		c, reason := b.change(r, f)
		if reason != "" {
			b.unresolved = append(b.unresolved, model.Unresolved{Finding: f, Reason: reason})
			continue
		}
		if f.Tag == tagDeploymentType {
			deployment = c.New
		}
		planned = append(planned, c)
	}

	route := model.RouteDirect
	if strings.EqualFold(deployment, "Automation") {
		route = model.RouteTerraform
	}
	if !b.writable(r, fs[0]) {
		route = model.RouteUnwritable
	}
	var repo *model.RepoRef
	if route == model.RouteTerraform {
		repo = repoOf(r)
	}
	for i := range planned {
		planned[i].Route, planned[i].Repo = route, repo
	}
	b.changes = append(b.changes, planned...)
}

// repoOf is the repository named by the resource's SCM, Org and Repo
// tags, or nil unless all three have a value.
func repoOf(r *model.Resource) *model.RepoRef {
	var vals [3]string
	for i, name := range []string{"SCM", "Org", "Repo"} {
		t, ok := currentTag(r, name)
		if !ok || strings.TrimSpace(t.Value) == "" {
			return nil
		}
		vals[i] = strings.TrimSpace(t.Value)
	}
	return &model.RepoRef{SCM: vals[0], Org: vals[1], Repo: vals[2]}
}

// currentDeployment is the resource's DeploymentType in dictionary
// spelling, or Manual when it has none.
func currentDeployment(dict *policy.Dictionary, r *model.Resource) string {
	if def, ok := dict.Lookup(tagDeploymentType); ok {
		if t, found := currentTag(r, tagDeploymentType); found {
			if v, ok := def.Accept(t.Value); ok {
				return v
			}
		}
	}
	return defaultDeployment
}

// writable reports whether tagherd writes this type: catalog partial types
// and GCP types outside the Compute, Database and Storage groups are only
// scanned.
func (b *planner) writable(r *model.Resource, f model.Finding) bool {
	if e, ok := b.set.Catalog.Lookup(r.Cloud, f.Type); ok && b.set.Catalog.Canonical(e).Taggable == catalog.Partial {
		return false
	}
	if r.Cloud != model.GCP {
		return true
	}
	for _, g := range f.Groups {
		for _, p := range gcpWriteGroups {
			if strings.HasPrefix(g, p) {
				return true
			}
		}
	}
	return false
}

// change returns the change for one finding, or why there is none.
func (b *planner) change(r *model.Resource, f model.Finding) (model.Change, string) {
	def, ok := b.set.Dict.Lookup(f.Tag)
	if !ok {
		return model.Change{}, ReasonNotFixable
	}
	c := model.Change{
		ResourceID: r.ID, Cloud: r.Cloud, Scope: r.Scope, Region: r.Region,
		Type: f.Type, NativeType: r.NativeType, Name: r.Name, Tag: def.Name,
	}
	current, found := currentTag(r, def.Name)
	c.Kind = kindFor(r.Cloud, def.Name, current, found)

	switch f.Code {
	case model.MissingTag, model.EmptyValue:
		cands := b.fillCandidates(r, f, def)
		v, rule, reason := pick(r.Cloud, c.Kind, cands)
		if reason != "" {
			return c, reason
		}
		c.New, c.Rule = v, rule
	case model.InvalidValue:
		var cands []candidate
		if v, ok := b.src.Set[def.Name]; ok {
			cands = append(cands, candidate{v, "set"})
		}
		if b.src.Synonyms != nil {
			if v, ok := b.src.Synonyms.Lookup(def.Name, f.Current); ok {
				cands = append(cands, candidate{v, "builtin:synonym"})
			}
		}
		if len(cands) == 0 {
			return c, ReasonNoSynonym
		}
		v, rule, reason := pick(r.Cloud, c.Kind, cands)
		if reason != "" {
			return c, reason
		}
		c.New, c.Rule = v, rule
	case model.CaseMismatch:
		// Proposed is the canonical value, or Key=Value when the key's
		// spelling is wrong. A new key is added beside the old one; tags
		// are never removed.
		c.New, c.Rule = f.Proposed, "builtin:case"
		if _, after, keyFix := strings.Cut(f.Proposed, "="); keyFix && strings.Contains(f.Current, "=") {
			c.New, found = after, false
			if r.Cloud == model.GCP {
				c.Kind = model.KindRMTag // label keys are always lowercase
			}
		}
	default:
		return c, ReasonNotFixable
	}

	if found {
		c.Tag, c.Action, c.Old, c.Expect = current.Key, model.ActionUpdate, current.Value, current.Value
	} else {
		c.Action = model.ActionAdd
	}
	return c, ""
}

type candidate struct{ value, rule string }

// fillCandidates lists the sources for a missing or empty tag in order:
// --set, defaults, parent tags, then the built-in rules.
func (b *planner) fillCandidates(r *model.Resource, f model.Finding, def *policy.TagDef) []candidate {
	var out []candidate
	if v, ok := b.src.Set[def.Name]; ok {
		out = append(out, candidate{v, "set"})
	}
	if b.src.Defaults != nil {
		if v, level, ok := b.src.Defaults.Lookup(r.Cloud, r.Scope, f.Type, f.Groups, def.Name); ok {
			out = append(out, candidate{v, "defaults:" + level})
		}
	}
	if def.Name != tagDeploymentType {
		for _, t := range b.src.Parents[ParentOf(*r)] {
			// A GCP label cannot hold an email address, so AdminEmail is
			// never inherited from project labels.
			if !strings.EqualFold(t.Key, def.Name) || (t.Kind == model.KindLabel && def.Name == tagAdminEmail) {
				continue
			}
			if v, ok := def.Accept(t.Value); ok {
				out = append(out, candidate{v, "parent"})
			}
			break
		}
	}
	switch def.Name {
	case tagDeploymentType:
		out = append(out, candidate{defaultDeployment, "builtin:deploymenttype"})
	case tagOwningOrg:
		if b.src.Ownership != nil {
			if v, ok := b.src.Ownership.Lookup(r.Cloud, r.Scope); ok {
				out = append(out, candidate{v, "builtin:ownership"})
			}
		}
	}
	return out
}

// pick returns the first candidate within the cloud's limits.
func pick(cloud model.Cloud, kind model.TagKind, cands []candidate) (string, string, string) {
	if len(cands) == 0 {
		return "", "", ReasonNoSource
	}
	for _, c := range cands {
		if fitsLimits(cloud, kind, c.value) {
			return c.value, c.rule, ""
		}
	}
	return "", "", ReasonLimit
}

// labelValue is what a GCP label value may hold once lowercased.
var labelValue = regexp.MustCompile(`^[\p{Ll}\p{Lo}\p{N}_-]{0,63}$`)

// fitsLimits applies the provider limits on a tag value.
func fitsLimits(cloud model.Cloud, kind model.TagKind, v string) bool {
	switch {
	case cloud == model.GCP && kind == model.KindLabel:
		return labelValue.MatchString(strings.ToLower(v))
	case cloud == model.GCP:
		return utf8.RuneCountInString(v) <= 63
	}
	return utf8.RuneCountInString(v) <= 256
}

// currentTag returns the tag the finding judged: among keys equal to name
// ignoring case, the exact spelling, then a Resource Manager tag, then the
// first. Updates go to that key, so no second spelling is added.
func currentTag(r *model.Resource, name string) (model.Tag, bool) {
	var cands []model.Tag
	for _, t := range r.Tags {
		if strings.EqualFold(t.Key, name) {
			cands = append(cands, t)
		}
	}
	if len(cands) == 0 {
		return model.Tag{}, false
	}
	return preferred(name, cands), true
}

// kindFor picks the GCP mechanism: the existing tag's, else a Resource
// Manager tag for AdminEmail and a label for everything else.
func kindFor(cloud model.Cloud, name string, current model.Tag, found bool) model.TagKind {
	switch {
	case cloud != model.GCP:
		return ""
	case found:
		return current.Kind
	case name == tagAdminEmail:
		return model.KindRMTag
	}
	return model.KindLabel
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
