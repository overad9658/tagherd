// Package gcp is the GCP discovery adapter: projects under the configured
// organization or folders, and resources from Cloud Asset Inventory, with
// both labels and Resource Manager tags.
package gcp

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/asset/apiv1/assetpb"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

// Options configure the adapter.
type Options struct {
	Scopes config.GCPScopes
	Log    *slog.Logger
}

// Provider implements provider.Provider for GCP.
type Provider struct {
	opts Options
	b    backend
	w    writeBackend // nil in discovery-only tests

	mu     sync.Mutex
	labels map[string][]model.Tag // project labels by project ID
}

var _ provider.Provider = (*Provider)(nil)

// backend is the Cloud Asset Inventory surface the adapter uses.
type backend interface {
	// Search runs SearchAllResources in scope ("organizations/1",
	// "folders/2", "projects/p") and returns every page.
	Search(ctx context.Context, scope string, assetTypes []string, query string, readMask []string) ([]*assetpb.ResourceSearchResult, error)
}

// New returns the adapter with Application Default Credentials, or the
// configured service account key.
func New(ctx context.Context, opts Options) (*Provider, error) {
	b, err := newSDKBackend(ctx, opts)
	if err != nil {
		return nil, err
	}
	w, err := newSDKWriter(ctx, opts)
	if err != nil {
		return nil, err
	}
	p := newProvider(opts, b)
	p.w = w
	return p, nil
}

func newProvider(opts Options, b backend) *Provider {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Provider{opts: opts, b: b, labels: map[string][]model.Tag{}}
}

func (p *Provider) Cloud() model.Cloud { return model.GCP }

const projectType = "cloudresourcemanager.googleapis.com/Project"

// readMask is the fields read for every resource. effectiveTags is named
// because it is not always returned by default.
var readMask = []string{"name", "assetType", "project", "displayName", "location", "labels", "createTime", "state", "effectiveTags", "additionalAttributes"}

// Scopes lists active projects under the organization and folders, filtered
// by the configured project globs, then by patterns. Without an
// organization or folders, the configured projects are literal IDs.
func (p *Provider) Scopes(ctx context.Context, patterns []string) ([]provider.Scope, error) {
	cfg := p.opts.Scopes
	var parents []string
	if cfg.OrganizationID != "" {
		parents = append(parents, "organizations/"+cfg.OrganizationID)
	}
	for _, f := range cfg.Folders {
		parents = append(parents, "folders/"+f)
	}
	var scopes []provider.Scope
	if len(parents) == 0 {
		for _, id := range cfg.Projects {
			if strings.ContainsAny(id, "*?[") {
				return nil, fmt.Errorf("scopes.gcp.projects: %q is a glob, which needs organization_id or folders", id)
			}
			scopes = append(scopes, provider.Scope{Cloud: model.GCP, ID: id})
		}
	}
	for _, parent := range parents {
		results, err := p.b.Search(ctx, parent, []string{projectType}, "state:ACTIVE", []string{"name", "displayName", "additionalAttributes"})
		if err != nil {
			return nil, fmt.Errorf("listing projects in %s: %w", parent, err)
		}
		for _, r := range results {
			id := projectID(r)
			if id == "" {
				continue
			}
			scopes = append(scopes, provider.Scope{Cloud: model.GCP, ID: id, Name: r.GetDisplayName()})
		}
	}
	if len(parents) > 0 {
		scopes = provider.SelectScopes(scopes, cfg.Projects)
	}
	scopes = provider.SelectScopes(scopes, patterns)
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].ID < scopes[j].ID })
	return slices.CompactFunc(scopes, func(a, b provider.Scope) bool { return a.ID == b.ID }), nil
}

// projectID reads a Project result's ID from its additional attributes.
func projectID(r *assetpb.ResourceSearchResult) string {
	if f := r.GetAdditionalAttributes().GetFields()["projectId"]; f != nil {
		return f.GetStringValue()
	}
	return ""
}

// versioned are asset types whose discriminator is read from
// versionedResources, which is large, so they get a search of their own.
var versioned = []string{"notebooks.googleapis.com/Instance"}

// Discover reads one project. One search failing is a partial error; the
// other still counts.
func (p *Provider) Discover(ctx context.Context, s provider.Scope, types []*catalog.Entry) (provider.Discovery, error) {
	split := provider.SplitTypes(types, func(string) bool { return false })
	d := provider.Discovery{Uncovered: split.Uncovered}
	var plain, withVersions []string
	for _, nt := range provider.NativeTypes(split.Bulk) {
		if slices.Contains(versioned, nt) {
			withVersions = append(withVersions, nt)
		} else {
			plain = append(plain, nt)
		}
	}
	scope := "projects/" + s.ID
	searches := 0
	var errs []error
	run := func(types, mask []string) {
		if len(types) == 0 {
			return
		}
		searches++
		results, err := p.b.Search(ctx, scope, types, "", mask)
		if err != nil {
			errs = append(errs, fmt.Errorf("searching Cloud Asset Inventory (%s): %w", strings.Join(types, ", "), err))
			return
		}
		for _, r := range results {
			d.Resources = append(d.Resources, toResource(s.ID, r))
		}
	}
	run(plain, readMask)
	run(withVersions, append(slices.Clone(readMask), "versionedResources"))
	if searches > 0 && len(errs) == searches {
		return d, errs[0] // nothing could be read
	}
	d.Errors = errs
	sort.SliceStable(d.Resources, func(i, j int) bool { return d.Resources[i].ID < d.Resources[j].ID })
	return d, nil
}

// toResource normalizes a search result. Labels and Resource Manager tags
// are both kept, by kind; RM tags include those inherited from ancestors.
func toResource(projectID string, r *assetpb.ResourceSearchResult) model.Resource {
	res := model.Resource{
		Cloud: model.GCP, Scope: projectID, Region: r.GetLocation(), NativeType: r.GetAssetType(),
		ID: r.GetName(), Name: firstNonEmpty(r.GetDisplayName(), lastSegment(r.GetName())),
		Parent: "projects/" + projectID, Attrs: map[string]string{},
	}
	if ct := r.GetCreateTime(); ct != nil {
		t := ct.AsTime().UTC()
		res.Created = &t
	}
	res.Tags = labels(r.GetLabels())
	res.Tags = append(res.Tags, rmTags(r)...)
	if slices.Contains(versioned, res.NativeType) {
		res.Attrs["workbench_version"] = workbenchVersion(r)
	}
	return res
}

func labels(m map[string]string) []model.Tag {
	out := make([]model.Tag, 0, len(m))
	for k, v := range m {
		out = append(out, model.Tag{Key: k, Value: v, Kind: model.KindLabel})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// rmTags returns effective Resource Manager tags by short name: tag key
// "123/DeploymentType" and value "123/DeploymentType/Manual" become
// DeploymentType=Manual. A binding on the resource itself wins over one
// inherited from an ancestor.
func rmTags(r *assetpb.ResourceSearchResult) []model.Tag {
	type found struct {
		value string
		own   bool
	}
	byKey := map[string]found{}
	for _, group := range r.GetEffectiveTags() {
		own := group.GetAttachedResource() == r.GetName()
		for _, t := range group.GetEffectiveTags() {
			key, value := lastSegment(t.GetTagKey()), lastSegment(t.GetTagValue())
			if prev, ok := byKey[key]; ok && (prev.own || !own) {
				continue
			}
			byKey[key] = found{value, own}
		}
	}
	out := make([]model.Tag, 0, len(byKey))
	for k, f := range byKey {
		out = append(out, model.Tag{Key: k, Value: f.value, Kind: model.KindRMTag})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// workbenchVersion tells Workbench instances (v2 API) from user-managed
// notebooks (v1 API), which share an asset type.
func workbenchVersion(r *assetpb.ResourceSearchResult) string {
	version := ""
	for _, v := range r.GetVersionedResources() {
		if v.GetVersion() > version {
			version = v.GetVersion()
		}
	}
	return version
}

// ParentTags returns the project's labels.
func (p *Provider) ParentTags(ctx context.Context, r model.Resource) ([]model.Tag, error) {
	p.mu.Lock()
	t, ok := p.labels[r.Scope]
	p.mu.Unlock()
	if ok {
		return t, nil
	}
	results, err := p.b.Search(ctx, "projects/"+r.Scope, []string{projectType}, "", []string{"name", "labels"})
	if err != nil {
		return nil, fmt.Errorf("project %s labels: %w", r.Scope, err)
	}
	t = []model.Tag{}
	if len(results) > 0 {
		t = labels(results[0].GetLabels())
	}
	p.mu.Lock()
	p.labels[r.Scope] = t
	p.mu.Unlock()
	return t, nil
}

func lastSegment(s string) string { return s[strings.LastIndex(s, "/")+1:] }

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// searchTimeout bounds one SearchAllResources page.
const searchTimeout = 2 * time.Minute
