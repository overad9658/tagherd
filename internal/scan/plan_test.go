package scan

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/overad9658/tagherd/internal/engine"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider/fake"
)

const planFixture = `
scopes:
  - cloud: AWS
    id: "1"
    tags: {Brand: Acme}
    resources:
      - {native_type: AWS::S3::Bucket, id: b1, name: b1, parent: "1", tags: {DeploymentType: Manual}}
  - cloud: Azure
    id: s
    parent_fail: "AuthorizationFailed"
    resources:
      - {native_type: microsoft.compute/virtualmachines, id: sa, name: sa, parent: /rg, tags: {DeploymentType: Manual}}
`

func TestPlan(t *testing.T) {
	set, _ := setup(t)
	env, err := fake.Parse([]byte(planFixture))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	p, res := Plan(context.Background(), set, providers(env), Options{Concurrency: 2}, PlanOptions{Now: now, Grace: time.Minute})
	if len(res.Findings) == 0 || len(p.ID) != 26 || !p.Created.Equal(now) {
		t.Fatalf("plan = %+v", p)
	}
	var brand *model.Change
	for i, c := range p.Changes {
		if c.ResourceID == "b1" && c.Tag == "Brand" {
			brand = &p.Changes[i]
		}
	}
	if brand == nil || brand.New != "Acme" || brand.Rule != "parent" {
		t.Errorf("AWS Brand from account tags: %+v", brand)
	}
	// The Azure parent could not be read: a warning, and its Brand stays open.
	if len(p.Summary.Warnings) != 1 || !strings.Contains(p.Summary.Warnings[0], "AuthorizationFailed") {
		t.Errorf("warnings = %v", p.Summary.Warnings)
	}
	open := false
	for _, u := range p.Unresolved {
		if u.ID == "sa" && u.Tag == "Brand" && u.Reason == engine.ReasonNoSource {
			open = true
		}
	}
	if !open {
		t.Errorf("unresolved = %+v", p.Unresolved)
	}
}
