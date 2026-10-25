package azure

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

func (s *stub) Caller(context.Context) (string, error) { return "ops@example.com (oid 1)", nil }

func (s *stub) Tags(_ context.Context, scope string) (map[string]string, error) {
	t, ok := s.tags[scope]
	if !ok {
		return nil, errors.New("ResourceNotFound")
	}
	return t, nil
}

func (s *stub) MergeTags(_ context.Context, scope string, tags map[string]string) error {
	if _, ok := s.tags[scope]; !ok {
		return errors.New("ResourceNotFound")
	}
	for k, v := range tags {
		s.tags[scope][k] = v
	}
	return nil
}

func TestWriter(t *testing.T) {
	const vm = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm"
	b := &stub{tags: map[string]map[string]string{vm: {"environment": "Production"}, "/subscriptions/s": {}}}
	p := newProvider(Options{}, b)
	ctx := context.Background()
	w, err := p.Writer(ctx, provider.Scope{ID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	tags, errs := w.Read(ctx, []provider.Target{{ID: vm}, {ID: "/subscriptions/s"}, {ID: "/nope"}})
	if len(tags[vm]) != 1 || tags["/subscriptions/s"] == nil || errs["/nope"] == nil {
		t.Errorf("read = %v, %v", tags, errs)
	}
	errs = w.Write(ctx, []provider.TagWrite{
		{Target: provider.Target{ID: vm}, Tags: []model.Tag{{Key: "DeploymentType", Value: "Manual"}}},
		{Target: provider.Target{ID: "/nope"}},
	})
	if len(errs) != 1 || b.tags[vm]["DeploymentType"] != "Manual" || b.tags[vm]["environment"] != "Production" {
		t.Errorf("write = %v, %v", errs, b.tags[vm])
	}
}

func TestTokenActor(t *testing.T) {
	jwt := func(payload string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".s"
	}
	if a, err := tokenActor(jwt(`{"oid":"o1","upn":"ops@example.com"}`)); err != nil || a != "ops@example.com (oid o1)" {
		t.Errorf("user = %q, %v", a, err)
	}
	if a, _ := tokenActor(jwt(`{"oid":"o2"}`)); a != "oid o2" {
		t.Errorf("bare = %q", a)
	}
	if _, err := tokenActor("nope"); err == nil || !strings.Contains(err.Error(), "format") {
		t.Errorf("bad token: %v", err)
	}
}
