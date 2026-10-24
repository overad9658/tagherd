package scan

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
	"github.com/overad9658/tagherd/internal/provider"
	"github.com/overad9658/tagherd/internal/provider/fake"
)

const fixture = "../../testdata/fixtures/scan.yaml"

func setup(t *testing.T) (*policy.Set, *fake.Env) {
	t.Helper()
	cat, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	set, rep := policy.Load("../../policy", cat)
	if set == nil {
		t.Fatalf("policy: %v", rep.Diagnostics)
	}
	env, err := fake.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return set, env
}

func providers(env *fake.Env) map[model.Cloud]provider.Provider {
	out := map[model.Cloud]provider.Provider{}
	for c, p := range env.Providers {
		out[c] = p
	}
	return out
}

// TestFixture is the phase 2 gate: the fixture produces every finding code.
func TestFixture(t *testing.T) {
	set, env := setup(t)
	r := Run(context.Background(), set, providers(env), Options{Concurrency: 4, Overrides: env.Wiz})

	wantCodes := map[model.Code]int{
		model.MissingTag: 3, model.EmptyValue: 1, model.InvalidValue: 3, model.CaseMismatch: 3,
		model.DuplicateKey: 1, model.InvalidDeploymentType: 1, model.NotTaggable: 1, model.UnmappedType: 1,
	}
	for c, n := range wantCodes {
		if r.Summary.ByCode[c] != n {
			t.Errorf("%s = %d, want %d", c, r.Summary.ByCode[c], n)
		}
	}
	wantCounts := model.Counts{Discovered: 14, OutOfScope: 1, Filtered: 1, NotTaggable: 1, Evaluated: 11, Compliant: 4}
	if r.Summary.Resources != wantCounts {
		t.Errorf("counts = %+v, want %+v", r.Summary.Resources, wantCounts)
	}
	if len(r.Summary.Unreadable) != 1 || r.Summary.Unreadable[0].Scope != "444455556666" {
		t.Errorf("unreadable = %+v", r.Summary.Unreadable)
	}
	if len(r.Summary.OverrideFallback) != 0 {
		t.Errorf("fallback = %v", r.Summary.OverrideFallback)
	}

	// The spec's example finding, field for field.
	want := model.Finding{
		Cloud: model.AWS, Scope: "111122223333", Region: "us-east-1", Group: "Database",
		Type: "AWS RDS PostgreSQL Instance", ID: "arn:aws:rds:us-east-1:111122223333:db:orders",
		Name: "orders", Code: model.CaseMismatch, Tag: "Environment",
		Current: "production", Proposed: "Production", Fixable: true,
	}
	found := false
	for _, f := range r.Findings {
		if f == want {
			found = true
		}
	}
	if !found {
		t.Errorf("spec example finding missing")
	}
}

func TestDeterministic(t *testing.T) {
	set, env := setup(t)
	var prev []byte
	for _, workers := range []int{1, 2, 8} {
		r := Run(context.Background(), set, providers(env), Options{Concurrency: workers, Overrides: env.Wiz})
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if prev != nil && string(b) != string(prev) {
			t.Fatalf("concurrency %d changed the result", workers)
		}
		prev = b
	}
}

func TestFilters(t *testing.T) {
	set, env := setup(t)
	ps := providers(env)
	ctx := context.Background()

	r := Run(ctx, set, ps, Options{Clouds: []model.Cloud{model.Azure}})
	if r.Summary.Resources.Discovered != 2 || len(r.Summary.Unreadable) != 0 {
		t.Errorf("--cloud azure: %+v", r.Summary.Resources)
	}

	r = Run(ctx, set, ps, Options{ScopePatterns: map[model.Cloud][]string{model.AWS: {"1111*"}}, Clouds: []model.Cloud{model.AWS}})
	if len(r.Summary.Unreadable) != 0 || r.Summary.Resources.Discovered != 10 {
		t.Errorf("--account 1111*: %+v %+v", r.Summary.Resources, r.Summary.Unreadable)
	}

	db, _ := set.Select([]string{"database"})
	r = Run(ctx, set, ps, Options{Groups: db, Clouds: []model.Cloud{model.AWS}})
	// Only RDS instances are requested from the provider: orders and legacy.
	if r.Summary.Resources.Discovered != 3 || r.Summary.Resources.OutOfScope != 1 {
		t.Errorf("--group database: %+v", r.Summary.Resources)
	}

	r = Run(ctx, set, ps, Options{Types: []string{"AWS S3 Bucket"}})
	if r.Summary.Resources.Evaluated != 1 || r.Summary.ByCode[model.UnmappedType] != 0 {
		t.Errorf("--type: %+v", r.Summary)
	}
}

func TestOverrideFallback(t *testing.T) {
	set, env := setup(t)
	// No runner: the AMI group falls back, so ami-0002's missing AdminEmail
	// is reported too.
	r := Run(context.Background(), set, providers(env), Options{})
	if r.Summary.ByCode[model.MissingTag] != 4 {
		t.Errorf("missing = %d, want 4", r.Summary.ByCode[model.MissingTag])
	}
	if len(r.Summary.OverrideFallback) != 1 {
		t.Errorf("fallback = %v", r.Summary.OverrideFallback)
	}

	failing, err := fake.Parse([]byte("wiz: {fail: timeout}\nscopes: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	r = Run(context.Background(), set, providers(env), Options{Overrides: failing.Wiz})
	if len(r.Summary.OverrideFallback) != 1 {
		t.Errorf("a failed query should fall back: %v", r.Summary.OverrideFallback)
	}
}
