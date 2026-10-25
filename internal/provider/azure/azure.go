// Package azure is the Azure discovery adapter: subscriptions in the tenant,
// and resources from Azure Resource Graph.
package azure

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

// Options configure the adapter.
type Options struct {
	Scopes config.AzureScopes
	Log    *slog.Logger
}

// Provider implements provider.Provider for Azure.
type Provider struct {
	opts Options
	b    backend

	mu   sync.Mutex
	tags map[string][]model.Tag // resource group tags by lower-cased ID
}

var _ provider.Provider = (*Provider)(nil)

// subscription is one subscription the credential can see.
type subscription struct {
	ID, Name, Tenant, State string
}

// backend is the Azure API surface the adapter uses.
type backend interface {
	Subscriptions(ctx context.Context) ([]subscription, error)
	// Query runs a Resource Graph query against one subscription and
	// returns every row, following skip tokens.
	Query(ctx context.Context, subscriptionID, query string) ([]map[string]any, error)

	// Caller names the credential's identity from its token.
	Caller(ctx context.Context) (string, error)
	// Tags reads the tags at a resource, resource group or subscription.
	Tags(ctx context.Context, scope string) (map[string]string, error)
	// MergeTags merges tags into that scope through the Tags API.
	MergeTags(ctx context.Context, scope string, tags map[string]string) error
}

// New returns the adapter with the Azure default credential chain.
func New(opts Options) (*Provider, error) {
	b, err := newSDKBackend(opts)
	if err != nil {
		return nil, err
	}
	return newProvider(opts, b), nil
}

func newProvider(opts Options, b backend) *Provider {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Provider{opts: opts, b: b, tags: map[string][]model.Tag{}}
}

func (p *Provider) Cloud() model.Cloud { return model.Azure }

// usable subscription states; Disabled and Deleted ones cannot be read.
var usable = []string{"Enabled", "Warned", "PastDue"}

// Scopes lists the tenant's usable subscriptions, filtered by the
// configured subscription globs, then by patterns.
func (p *Provider) Scopes(ctx context.Context, patterns []string) ([]provider.Scope, error) {
	subs, err := p.b.Subscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing subscriptions: %w", err)
	}
	tenant := p.opts.Scopes.TenantID
	var scopes []provider.Scope
	for _, s := range subs {
		if !slices.Contains(usable, s.State) || (tenant != "" && !strings.EqualFold(s.Tenant, tenant)) {
			continue
		}
		scopes = append(scopes, provider.Scope{Cloud: model.Azure, ID: s.ID, Name: s.Name})
	}
	scopes = provider.SelectScopes(provider.SelectScopes(scopes, p.opts.Scopes.Subscriptions), patterns)
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].ID < scopes[j].ID })
	return scopes, nil
}

// containerTypes live in Resource Graph's resourcecontainers table.
var containerTypes = []string{
	"microsoft.resources/subscriptions",
	"microsoft.resources/subscriptions/resourcegroups",
}

// Discover reads one subscription from Resource Graph. The resources query
// failing fails the scope; the containers query failing is a partial error.
func (p *Provider) Discover(ctx context.Context, s provider.Scope, types []*catalog.Entry) (provider.Discovery, error) {
	split := provider.SplitTypes(types, func(string) bool { return false })
	d := provider.Discovery{Uncovered: split.Uncovered}
	var plain, containers []string
	for _, nt := range provider.NativeTypes(split.Bulk) {
		nt = strings.ToLower(nt)
		if slices.Contains(containerTypes, nt) {
			containers = append(containers, nt)
		} else {
			plain = append(plain, nt)
		}
	}
	if len(plain) > 0 {
		rows, err := p.b.Query(ctx, s.ID, resourcesQuery(plain))
		if err != nil {
			return d, fmt.Errorf("querying Resource Graph: %w", err)
		}
		d.Resources = append(d.Resources, toResources(s.ID, rows)...)
	}
	if len(containers) > 0 {
		rows, err := p.b.Query(ctx, s.ID, containersQuery(containers))
		if err != nil {
			d.Errors = append(d.Errors, fmt.Errorf("querying Resource Graph resource containers: %w", err))
		} else {
			d.Resources = append(d.Resources, toResources(s.ID, rows)...)
		}
	}
	sort.SliceStable(d.Resources, func(i, j int) bool { return d.Resources[i].ID < d.Resources[j].ID })
	return d, nil
}

func inList(types []string) string {
	q := make([]string, len(types))
	for i, t := range types {
		q[i] = "'" + t + "'"
	}
	return strings.Join(q, ", ")
}

// resourcesQuery selects resources of the given types. kind and sku.name
// feed the catalog's discriminators (App Service vs Function, CDN vs Front
// Door, OpenAI).
func resourcesQuery(types []string) string {
	return "resources\n" +
		"| where type in~ (" + inList(types) + ")\n" +
		"| project id, name, type = tolower(type), location, kind, sku = tostring(sku.name), tags, resourceGroup, subscriptionId\n" +
		"| order by id asc"
}

func containersQuery(types []string) string {
	return "resourcecontainers\n" +
		"| where type in~ (" + inList(types) + ")\n" +
		"| project id, name, type = tolower(type), location, tags, resourceGroup, subscriptionId\n" +
		"| order by id asc"
}

// toResources normalizes Resource Graph rows.
func toResources(sub string, rows []map[string]any) []model.Resource {
	out := make([]model.Resource, 0, len(rows))
	for _, row := range rows {
		str := func(k string) string { v, _ := row[k].(string); return v }
		r := model.Resource{
			Cloud: model.Azure, Scope: sub, Region: str("location"), NativeType: str("type"),
			ID: str("id"), Name: str("name"), Tags: tagsOf(row["tags"]),
			Attrs: map[string]string{"kind": str("kind"), "sku": str("sku")},
		}
		switch rg := str("resourceGroup"); {
		case r.NativeType == "microsoft.resources/subscriptions":
		case r.NativeType == "microsoft.resources/subscriptions/resourcegroups":
			r.Parent = "/subscriptions/" + sub
		case rg != "":
			r.Parent = "/subscriptions/" + sub + "/resourceGroups/" + rg
		}
		out = append(out, r)
	}
	return out
}

// tagsOf converts a Resource Graph tags object to sorted tags.
func tagsOf(v any) []model.Tag {
	m, _ := v.(map[string]any)
	out := make([]model.Tag, 0, len(m))
	for k, val := range m {
		s, _ := val.(string)
		out = append(out, model.Tag{Key: k, Value: s, Kind: model.KindTag})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ParentTags returns the tags of the resource's resource group.
func (p *Provider) ParentTags(ctx context.Context, r model.Resource) ([]model.Tag, error) {
	if !strings.Contains(strings.ToLower(r.Parent), "/resourcegroups/") {
		return nil, nil
	}
	key := strings.ToLower(r.Parent)
	p.mu.Lock()
	t, ok := p.tags[key]
	p.mu.Unlock()
	if ok {
		return t, nil
	}
	q := "resourcecontainers\n| where type == 'microsoft.resources/subscriptions/resourcegroups' and id =~ '" +
		strings.ReplaceAll(r.Parent, "'", "") + "'\n| project tags"
	rows, err := p.b.Query(ctx, r.Scope, q)
	if err != nil {
		return nil, fmt.Errorf("resource group tags: %w", err)
	}
	t = []model.Tag{}
	if len(rows) > 0 {
		t = tagsOf(rows[0]["tags"])
	}
	p.mu.Lock()
	p.tags[key] = t
	p.mu.Unlock()
	return t, nil
}

// Writer returns a tag writer with the same credential as discovery,
// which needs Tag Contributor on the subscription.
func (p *Provider) Writer(ctx context.Context, s provider.Scope) (provider.TagWriter, error) {
	actor, err := p.b.Caller(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the Azure caller identity: %w", err)
	}
	return &writer{b: p.b, actor: actor}, nil
}

type writer struct {
	b     backend
	actor string
}

func (w *writer) Actor(context.Context) (string, error) { return w.actor, nil }

func (w *writer) Read(ctx context.Context, targets []provider.Target) (map[string][]model.Tag, map[string]error) {
	out, errs := map[string][]model.Tag{}, map[string]error{}
	for _, t := range targets {
		tags, err := w.b.Tags(ctx, t.ID)
		if err != nil {
			errs[t.ID] = err
			continue
		}
		m := make(map[string]any, len(tags))
		for k, v := range tags {
			m[k] = v
		}
		out[t.ID] = tagsOf(m)
	}
	return out, errs
}

// Write merges each resource's tags with one Tags API call. Tag names
// ignore case, so the merge updates a key in whatever case it has.
func (w *writer) Write(ctx context.Context, writes []provider.TagWrite) map[string]error {
	errs := map[string]error{}
	for _, wr := range writes {
		tags := make(map[string]string, len(wr.Tags))
		for _, t := range wr.Tags {
			tags[t.Key] = t.Value
		}
		if err := w.b.MergeTags(ctx, wr.Target.ID, tags); err != nil {
			errs[wr.Target.ID] = err
		}
	}
	return errs
}
