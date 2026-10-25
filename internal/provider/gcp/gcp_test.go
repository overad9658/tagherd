package gcp

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/asset/apiv1/assetpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

type search struct {
	scope string
	types []string
	mask  []string
}

type stub struct {
	results map[string][]*assetpb.ResourceSearchResult // by scope
	fail    map[string]error                           // by first asset type
	calls   []search
}

func (s *stub) Search(_ context.Context, scope string, types []string, _ string, mask []string) ([]*assetpb.ResourceSearchResult, error) {
	s.calls = append(s.calls, search{scope, types, mask})
	if err := s.fail[types[0]]; err != nil {
		return nil, err
	}
	var out []*assetpb.ResourceSearchResult
	for _, r := range s.results[scope] {
		if slices.Contains(types, r.GetAssetType()) {
			out = append(out, r)
		}
	}
	return out, nil
}

func project(id, name string) *assetpb.ResourceSearchResult {
	attrs, _ := structpb.NewStruct(map[string]any{"projectId": id})
	return &assetpb.ResourceSearchResult{AssetType: projectType, Name: "//cloudresourcemanager.googleapis.com/projects/" + id, DisplayName: name, AdditionalAttributes: attrs}
}

func entries(t *testing.T, displays ...string) []*catalog.Entry {
	t.Helper()
	cat, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	var out []*catalog.Entry
	for _, d := range displays {
		e, ok := cat.Lookup(model.GCP, d)
		if !ok {
			t.Fatalf("no catalog entry %q", d)
		}
		out = append(out, e)
	}
	return out
}

func TestScopes(t *testing.T) {
	b := &stub{results: map[string][]*assetpb.ResourceSearchResult{
		"organizations/1": {project("prod-a", "Prod A"), project("dev-b", "Dev B")},
		"folders/9":       {project("prod-a", "Prod A"), project("lab-c", "Lab")},
	}}
	p := newProvider(Options{Scopes: config.GCPScopes{OrganizationID: "1", Folders: []string{"9"}, Projects: []string{"prod-*", "lab-*"}}}, b)
	got, err := p.Scopes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if !slices.Equal(ids, []string{"lab-c", "prod-a"}) {
		t.Errorf("ids = %v", ids)
	}
	p = newProvider(Options{Scopes: config.GCPScopes{Projects: []string{"one", "two"}}}, b)
	if got, _ := p.Scopes(context.Background(), []string{"tw*"}); len(got) != 1 || got[0].ID != "two" {
		t.Errorf("literal projects = %+v", got)
	}
	p = newProvider(Options{Scopes: config.GCPScopes{Projects: []string{"*"}}}, b)
	if _, err := p.Scopes(context.Background(), nil); err == nil {
		t.Error("a glob without an organization should fail")
	}
}

func TestDiscover(t *testing.T) {
	created := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	vm := &assetpb.ResourceSearchResult{
		Name:       "//compute.googleapis.com/projects/prod-a/zones/us-central1-a/instances/web-1",
		AssetType:  "compute.googleapis.com/Instance",
		Location:   "us-central1-a",
		CreateTime: timestamppb.New(created),
		Labels:     map[string]string{"environment": "production", "deploymenttype": "manual"},
		EffectiveTags: []*assetpb.EffectiveTagDetails{
			{AttachedResource: proto.String("//cloudresourcemanager.googleapis.com/projects/prod-a"), EffectiveTags: []*assetpb.Tag{
				{TagKey: proto.String("1/AdminEmail"), TagValue: proto.String("1/AdminEmail/old")},
				{TagKey: proto.String("1/OwningOrg"), TagValue: proto.String("1/OwningOrg/Platform")},
			}},
			{AttachedResource: proto.String("//compute.googleapis.com/projects/prod-a/zones/us-central1-a/instances/web-1"), EffectiveTags: []*assetpb.Tag{
				{TagKey: proto.String("1/AdminEmail"), TagValue: proto.String("1/AdminEmail/team")},
			}},
		},
	}
	nb := &assetpb.ResourceSearchResult{
		Name: "//notebooks.googleapis.com/projects/prod-a/locations/us-central1-a/instances/nb", AssetType: "notebooks.googleapis.com/Instance",
		VersionedResources: []*assetpb.VersionedResource{{Version: "v2"}},
	}
	b := &stub{results: map[string][]*assetpb.ResourceSearchResult{"projects/prod-a": {vm, nb}}}
	p := newProvider(Options{}, b)
	types := entries(t, "GCP Compute Instance", "GCP Vertex AI Workbench Instance", "GCP Vertex AI Workbench User Managed Notebook")
	d, err := p.Discover(context.Background(), provider.Scope{Cloud: model.GCP, ID: "prod-a"}, types)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 2 || slices.Contains(b.calls[0].mask, "versionedResources") || !slices.Contains(b.calls[1].mask, "versionedResources") {
		t.Errorf("calls = %+v", b.calls)
	}
	if len(d.Resources) != 2 {
		t.Fatalf("resources = %+v", d.Resources)
	}
	web := d.Resources[0]
	if web.Name != "web-1" || web.Region != "us-central1-a" || web.Created == nil || !web.Created.Equal(created) || web.Parent != "projects/prod-a" {
		t.Errorf("vm = %+v", web)
	}
	want := []model.Tag{
		{Key: "deploymenttype", Value: "manual", Kind: model.KindLabel},
		{Key: "environment", Value: "production", Kind: model.KindLabel},
		{Key: "AdminEmail", Value: "team", Kind: model.KindRMTag}, // own binding wins
		{Key: "OwningOrg", Value: "Platform", Kind: model.KindRMTag},
	}
	if !slices.Equal(web.Tags, want) {
		t.Errorf("tags = %+v", web.Tags)
	}
	cat, _ := catalog.Default()
	if e, err := cat.Classify(model.GCP, d.Resources[1].NativeType, d.Resources[1].Attrs); err != nil || e.Display != "GCP Vertex AI Workbench Instance" {
		t.Errorf("notebook classified as %v, %v", e, err)
	}
}

func TestDiscoverFailures(t *testing.T) {
	b := &stub{fail: map[string]error{"notebooks.googleapis.com/Instance": errors.New("PermissionDenied")}}
	p := newProvider(Options{}, b)
	d, err := p.Discover(context.Background(), provider.Scope{ID: "p"}, entries(t, "GCP Bucket", "GCP Vertex AI Workbench Instance"))
	if err != nil || len(d.Errors) != 1 {
		t.Errorf("one search failing is partial: %v, %v", err, d.Errors)
	}
	b.fail["storage.googleapis.com/Bucket"] = errors.New("PermissionDenied")
	if _, err := p.Discover(context.Background(), provider.Scope{ID: "p"}, entries(t, "GCP Bucket", "GCP Vertex AI Workbench Instance")); err == nil {
		t.Error("every search failing fails the scope")
	}
}

func TestParentTags(t *testing.T) {
	pr := project("prod-a", "Prod A")
	pr.Labels = map[string]string{"brand": "acme"}
	b := &stub{results: map[string][]*assetpb.ResourceSearchResult{"projects/prod-a": {pr}}}
	p := newProvider(Options{}, b)
	for range 2 {
		tags, err := p.ParentTags(context.Background(), model.Resource{Scope: "prod-a"})
		if err != nil || len(tags) != 1 || tags[0].Kind != model.KindLabel {
			t.Fatalf("tags = %v, %v", tags, err)
		}
	}
	if len(b.calls) != 1 {
		t.Errorf("not cached: %d calls", len(b.calls))
	}
}
