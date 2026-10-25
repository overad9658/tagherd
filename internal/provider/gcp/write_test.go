package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

type wstub struct {
	labels   map[string]map[string]string
	bindings map[string][]rmBinding
	values   map[string]string // namespaced name -> tagValues/N
	calls    []string
}

func (s *wstub) Caller(context.Context) (string, error) { return "sa@p.iam.gserviceaccount.com", nil }

func (s *wstub) Labels(_ context.Context, t provider.Target) (map[string]string, error) {
	l, ok := s.labels[t.ID]
	if !ok {
		return nil, errors.New("notFound")
	}
	return l, nil
}

func (s *wstub) SetLabels(_ context.Context, t provider.Target, labels map[string]string) error {
	s.calls = append(s.calls, "set-labels")
	s.labels[t.ID] = labels
	return nil
}

func (s *wstub) EffectiveTags(_ context.Context, t provider.Target) ([]rmBinding, error) {
	return s.bindings[t.ID], nil
}

func (s *wstub) TagValue(_ context.Context, ns, key, value string) (string, error) {
	name := ns + "/" + key + "/" + value
	if v, ok := s.values[name]; ok {
		return v, nil
	}
	if key == "Missing" {
		return "", errors.New("tag key does not exist")
	}
	s.calls = append(s.calls, "create "+name)
	s.values[name] = "tagValues/new"
	return "tagValues/new", nil
}

func (s *wstub) Bind(_ context.Context, _ provider.Target, v string) error {
	s.calls = append(s.calls, "bind "+v)
	return nil
}

func (s *wstub) Unbind(_ context.Context, _ provider.Target, v string) error {
	s.calls = append(s.calls, "unbind "+v)
	return nil
}

func TestWriter(t *testing.T) {
	const vm = "//compute.googleapis.com/projects/p/zones/us-central1-a/instances/vm"
	ws := &wstub{
		labels: map[string]map[string]string{vm: {"environment": "prod", "team": "x"}},
		bindings: map[string][]rmBinding{vm: {
			{Key: "AdminEmail", Value: "old@example.com", ValueName: "tagValues/1"},
			{Key: "OwningOrg", Value: "ITOps", ValueName: "tagValues/2", Inherited: true},
		}},
		values: map[string]string{"123/AdminEmail/ops@example.com": "tagValues/3"},
	}
	p := newProvider(Options{Scopes: config.GCPScopes{OrganizationID: "123"}}, &stub{})
	p.w = ws
	ctx := context.Background()
	w, err := p.Writer(ctx, provider.Scope{Cloud: model.GCP, ID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	target := provider.Target{ID: vm, NativeType: "compute.googleapis.com/Instance", Region: "us-central1-a"}
	tags, errs := w.Read(ctx, []provider.Target{target, {ID: "//x/nope"}})
	if errs["//x/nope"] == nil || len(tags[vm]) != 4 {
		t.Fatalf("read = %v, %v", tags, errs)
	}

	errs = w.Write(ctx, []provider.TagWrite{{Target: target, Tags: []model.Tag{
		{Key: "environment", Value: "production", Kind: model.KindLabel},
		{Key: "AdminEmail", Value: "ops@example.com", Kind: model.KindRMTag}, // replaces the own binding
		{Key: "OwningOrg", Value: "Platform", Kind: model.KindRMTag},         // overrides the inherited one
	}}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if l := ws.labels[vm]; l["environment"] != "production" || l["team"] != "x" {
		t.Errorf("labels = %v (must merge)", l)
	}
	want := []string{"set-labels", "unbind tagValues/1", "bind tagValues/3", "create 123/OwningOrg/Platform", "bind tagValues/new"}
	if !slices.Equal(ws.calls, want) {
		t.Errorf("calls = %q", ws.calls)
	}

	errs = w.Write(ctx, []provider.TagWrite{{Target: target, Tags: []model.Tag{{Key: "Missing", Value: "v", Kind: model.KindRMTag}}}})
	if errs[vm] == nil || !strings.Contains(errs[vm].Error(), "123/Missing/v") {
		t.Errorf("missing key: %v", errs)
	}
}

func TestWriterNamespace(t *testing.T) {
	p := newProvider(Options{}, &stub{})
	p.w = &wstub{}
	w, _ := p.Writer(context.Background(), provider.Scope{ID: "proj"})
	if w.(*writer).namespace != "proj" {
		t.Errorf("namespace = %q, want the project", w.(*writer).namespace)
	}
	if _, err := newProvider(Options{}, &stub{}).Writer(context.Background(), provider.Scope{}); err == nil {
		t.Error("no write backend should fail")
	}
}

func TestResourcePath(t *testing.T) {
	path, err := resourcePath("//compute.googleapis.com/projects/p/zones/z/instances/i")
	if err != nil || path != "projects/p/zones/z/instances/i" {
		t.Errorf("path = %q, %v", path, err)
	}
	if s := segments(path); s["zones"] != "z" || s["instances"] != "i" {
		t.Errorf("segments = %v", s)
	}
	if _, err := resourcePath("projects/p"); err == nil {
		t.Error("not a full name")
	}
}
