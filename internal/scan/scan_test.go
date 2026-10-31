package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
	"github.com/overad9658/tagherd/internal/provider"
	"github.com/overad9658/tagherd/internal/provider/fake"
	"github.com/overad9658/tagherd/internal/wiz"
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
		model.MissingTag: 9, model.EmptyValue: 1, model.InvalidValue: 5, model.CaseMismatch: 3,
		model.DuplicateKey: 1, model.InvalidDeploymentType: 1, model.NotTaggable: 1,
		// The fixture's one uncovered type, plus each catalog gap once per
		// cloud the fixture has scopes for.
		model.UnmappedType: 1 + len(catalogGaps(set, []model.Cloud{model.AWS, model.Azure, model.GCP})),
	}
	for c, n := range wantCodes {
		if r.Summary.ByCode[c] != n {
			t.Errorf("%s = %d, want %d", c, r.Summary.ByCode[c], n)
		}
	}
	wantCounts := model.Counts{Discovered: 20, OutOfScope: 1, Filtered: 1, NotTaggable: 1, Evaluated: 17, Compliant: 4}
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
		Current: "production", Proposed: "Production", Fixable: true, Groups: []string{"Database"},
	}
	found := false
	for _, f := range r.Findings {
		if reflect.DeepEqual(f, want) {
			found = true
		}
	}
	if !found {
		t.Errorf("spec example finding missing")
	}
}

// catalogGaps returns the types with no discovery adapter that the groups name.
func catalogGaps(set *policy.Set, clouds []model.Cloud) []string {
	var out []string
	for _, cloud := range model.Clouds {
		if clouds != nil && !slices.Contains(clouds, cloud) {
			continue
		}
		for _, e := range typesFor(set.Groups, cloud, nil) {
			if mode, _ := e.DiscoveryMode(); mode == catalog.DiscoverNone {
				out = append(out, e.Display)
			}
		}
	}
	return out
}

func TestCatalogGapsOncePerCloud(t *testing.T) {
	set, env := setup(t)
	in := Discover(context.Background(), set, providers(env), Options{Clouds: []model.Cloud{model.AWS}})
	gaps := catalogGaps(set, []model.Cloud{model.AWS})
	if len(gaps) == 0 {
		t.Fatal("expected AWS catalog gaps")
	}
	n := 0
	for _, u := range in.Unmapped {
		if u.Scope == "*" {
			n++
			if u.Cloud != model.AWS {
				t.Errorf("gap for %s with --cloud aws", u.Cloud)
			}
		}
	}
	if n != len(gaps) {
		t.Errorf("%d cloud-wide gaps, want %d (one per type, not per scope)", n, len(gaps))
	}
}

func TestPartialErrors(t *testing.T) {
	set, _ := setup(t)
	p := &partial{}
	in := Discover(context.Background(), set, map[model.Cloud]provider.Provider{model.GCP: p}, Options{Clouds: []model.Cloud{model.GCP}})
	if len(in.Resources) != 1 || len(in.Unreadable) != 1 || in.Unreadable[0].Scope != "proj-a" {
		t.Errorf("resources %d, unreadable %+v", len(in.Resources), in.Unreadable)
	}
}

// partial is a provider whose one scope reads with a per-type error.
type partial struct{ provider.Provider }

func (*partial) Scopes(context.Context, []string) ([]provider.Scope, error) {
	return []provider.Scope{{Cloud: model.GCP, ID: "proj-a"}}, nil
}

func (*partial) Discover(context.Context, provider.Scope, []*catalog.Entry) (provider.Discovery, error) {
	return provider.Discovery{
		Resources: []model.Resource{{Cloud: model.GCP, Scope: "proj-a", NativeType: "storage.googleapis.com/Bucket", ID: "b", Name: "b"}},
		Errors:    []error{errors.New("GCP Cloud SQL Instance: permission denied")},
	}, nil
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
	if len(r.Summary.Unreadable) != 0 || r.Summary.Resources.Discovered != 16 {
		t.Errorf("--account 1111*: %+v %+v", r.Summary.Resources, r.Summary.Unreadable)
	}

	db, _ := set.Select([]string{"database"})
	r = Run(ctx, set, ps, Options{Groups: db, Clouds: []model.Cloud{model.AWS}})
	// Only the group's types are requested: three DynamoDB tables and the
	// RDS instances orders and legacy.
	if r.Summary.Resources.Discovered != 5 || r.Summary.Resources.OutOfScope != 1 {
		t.Errorf("--group database: %+v", r.Summary.Resources)
	}

	r = Run(ctx, set, ps, Options{Types: []string{"AWS S3 Bucket"}})
	if r.Summary.Resources.Evaluated != 3 || r.Summary.ByCode[model.UnmappedType] != 0 {
		t.Errorf("--type: %+v", r.Summary)
	}
}

func TestOverrideFallback(t *testing.T) {
	set, env := setup(t)
	// No runner: the AMI group falls back, so ami-0002's missing AdminEmail
	// is reported too.
	var logs bytes.Buffer
	r := Run(context.Background(), set, providers(env), Options{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if n := strings.Count(logs.String(), "no Wiz connection"); n != 1 {
		t.Errorf("the missing-Wiz warning was logged %d times, want once", n)
	}
	if r.Summary.ByCode[model.MissingTag] != 10 {
		t.Errorf("missing = %d, want 10", r.Summary.ByCode[model.MissingTag])
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

// shortIDs reports the fixture's AMI as Wiz may: a bare, uppercase AMI ID.
type shortIDs struct{}

func (shortIDs) Run(context.Context, *wiz.Query, []provider.Scope) (map[string]bool, error) {
	return map[string]bool{"AMI-0001": true, "sc-123": true}, nil
}

func TestOverrideJoinsByAMIID(t *testing.T) {
	set, env := setup(t)
	short := Run(context.Background(), set, providers(env), Options{Overrides: shortIDs{}})
	full := Run(context.Background(), set, providers(env), Options{Overrides: env.Wiz})
	if !reflect.DeepEqual(short.Findings, full.Findings) || len(short.Summary.OverrideFallback) != 0 {
		t.Errorf("a bare AMI ID should match the discovered ARN: %d vs %d findings", len(short.Findings), len(full.Findings))
	}
	if got := joinIDs(map[string]bool{"ami-0001": true}, model.Azure, []model.Resource{{Cloud: model.AWS, ID: "arn:aws:ec2:r:1:image/ami-0001"}}); len(got) != 0 {
		t.Errorf("ids join within one cloud only: %v", got)
	}
}
