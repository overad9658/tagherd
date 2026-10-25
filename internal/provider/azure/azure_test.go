package azure

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

type stub struct {
	subs    []subscription
	rows    map[string][]map[string]any // by table
	failing string                      // table whose query fails
	queries []string
}

func (s *stub) Subscriptions(context.Context) ([]subscription, error) { return s.subs, nil }

func (s *stub) Query(_ context.Context, _ string, q string) ([]map[string]any, error) {
	s.queries = append(s.queries, q)
	table := strings.SplitN(q, "\n", 2)[0]
	if table == s.failing {
		return nil, errors.New("RateLimiting")
	}
	return s.rows[table], nil
}

func entries(t *testing.T, displays ...string) []*catalog.Entry {
	t.Helper()
	cat, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	var out []*catalog.Entry
	for _, d := range displays {
		e, ok := cat.Lookup(model.Azure, d)
		if !ok {
			t.Fatalf("no catalog entry %q", d)
		}
		out = append(out, e)
	}
	return out
}

func TestScopes(t *testing.T) {
	b := &stub{subs: []subscription{
		{ID: "sub-1", Name: "prod", Tenant: "T1", State: "Enabled"},
		{ID: "sub-2", Name: "old", Tenant: "T1", State: "Disabled"},
		{ID: "sub-3", Name: "dev", Tenant: "T2", State: "Enabled"},
		{ID: "sub-4", Name: "test", Tenant: "t1", State: "Warned"},
	}}
	p := newProvider(Options{Scopes: config.AzureScopes{TenantID: "T1", Subscriptions: []string{"*"}}}, b)
	got, err := p.Scopes(context.Background(), nil)
	if err != nil || len(got) != 2 || got[0].ID != "sub-1" || got[1].ID != "sub-4" {
		t.Fatalf("scopes = %+v, %v", got, err)
	}
	if got, _ := p.Scopes(context.Background(), []string{"te*"}); len(got) != 1 || got[0].Name != "test" {
		t.Errorf("--subscription te* = %+v", got)
	}
}

func TestDiscover(t *testing.T) {
	b := &stub{rows: map[string][]map[string]any{
		"resources": {
			{"id": "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Web/sites/api", "name": "api", "type": "microsoft.web/sites",
				"location": "eastus", "kind": "functionapp,linux", "resourceGroup": "rg", "tags": map[string]any{"env": "Production", "Owner": "x"}},
		},
		"resourcecontainers": {
			{"id": "/subscriptions/sub-1/resourceGroups/rg", "name": "rg", "type": "microsoft.resources/subscriptions/resourcegroups", "location": "eastus"},
		},
	}}
	p := newProvider(Options{}, b)
	types := entries(t, "Azure Function", "Azure Resource Group", "Azure Microsoft Entra ID User")
	d, err := p.Discover(context.Background(), provider.Scope{Cloud: model.Azure, ID: "sub-1"}, types)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.queries) != 2 || !strings.Contains(b.queries[0], "'microsoft.web/sites'") || !strings.Contains(b.queries[1], "resourcegroups") {
		t.Errorf("queries = %q", b.queries)
	}
	if len(d.Resources) != 2 || len(d.Uncovered) != 1 {
		t.Fatalf("discovery = %+v", d)
	}
	rg, fn := d.Resources[0], d.Resources[1]
	if fn.Attrs["kind"] != "functionapp,linux" || fn.Parent != "/subscriptions/sub-1/resourceGroups/rg" || fn.Region != "eastus" {
		t.Errorf("function = %+v", fn)
	}
	if len(fn.Tags) != 2 || fn.Tags[0].Key != "Owner" || fn.Tags[1].Value != "Production" {
		t.Errorf("tags = %+v", fn.Tags)
	}
	if rg.Parent != "/subscriptions/sub-1" || rg.Tags == nil {
		t.Errorf("resource group = %+v", rg)
	}

	// The catalog classifies the row as a Function, not an App Service.
	cat, _ := catalog.Default()
	if e, err := cat.Classify(model.Azure, fn.NativeType, fn.Attrs); err != nil || e.Display != "Azure Function" {
		t.Errorf("classify = %v, %v", e, err)
	}
}

func TestDiscoverFailures(t *testing.T) {
	b := &stub{failing: "resourcecontainers"}
	p := newProvider(Options{}, b)
	d, err := p.Discover(context.Background(), provider.Scope{ID: "sub-1"}, entries(t, "Azure Storage Account", "Azure Subscription"))
	if err != nil || len(d.Errors) != 1 {
		t.Errorf("containers failing is partial: err %v, errors %v", err, d.Errors)
	}
	b.failing = "resources"
	if _, err := p.Discover(context.Background(), provider.Scope{ID: "sub-1"}, entries(t, "Azure Storage Account")); err == nil {
		t.Error("resources failing fails the scope")
	}
}

func TestParentTags(t *testing.T) {
	b := &stub{rows: map[string][]map[string]any{"resourcecontainers": {{"tags": map[string]any{"Brand": "Acme"}}}}}
	p := newProvider(Options{}, b)
	r := model.Resource{Scope: "sub-1", Parent: "/subscriptions/sub-1/resourceGroups/rg"}
	for range 2 {
		tags, err := p.ParentTags(context.Background(), r)
		if err != nil || len(tags) != 1 || tags[0].Value != "Acme" {
			t.Fatalf("tags = %v, %v", tags, err)
		}
	}
	if len(b.queries) != 1 {
		t.Errorf("parent tags not cached: %d queries", len(b.queries))
	}
	if tags, _ := p.ParentTags(context.Background(), model.Resource{Parent: "/subscriptions/sub-1"}); tags != nil {
		t.Errorf("no resource group: %v", tags)
	}
}
