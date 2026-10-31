package engine

import (
	"encoding/json"
	"testing"
	"testing/fstest"
	"time"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
	"github.com/overad9658/tagherd/internal/values"
)

var planPolicy = fstest.MapFS{
	"tags.json": {Data: []byte(`{"Environment": ["Production", "Staging"], "DeploymentType": ["Manual", "Automation"],
		"OwningOrg": ["ITOps", "Platform"], "AdminEmail": null, "Brand": null}`)},
	"db.json": {Data: []byte(`{"name": "Database", "description": "d", "tags": ["Environment", "DeploymentType", "OwningOrg", "AdminEmail", "Brand"],
		"target": {"AWS": ["AWS DynamoDB Table"], "Azure": ["Azure Compute Virtual Machine"], "GCP": [], "OCI": []}}`)},
	"compute.json": {Data: []byte(`{"name": "Compute (only machines)", "description": "c", "tags": ["Environment", "DeploymentType", "OwningOrg", "AdminEmail", "Brand"],
		"target": {"AWS": [], "Azure": [], "GCP": ["GCP Compute Instance", "GCP Compute Instance Template"], "OCI": []}}`)},
	"sec.json": {Data: []byte(`{"name": "Security", "description": "s", "tags": ["DeploymentType", "Brand"],
		"target": {"AWS": [], "Azure": [], "GCP": ["GCP Secret Manager Secret"], "OCI": []}}`)},
}

func loadPlanPolicy(t *testing.T) *policy.Set {
	t.Helper()
	cat, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	set, rep := policy.LoadFS(planPolicy, cat)
	if set == nil {
		t.Fatalf("policy: %v", rep.Diagnostics)
	}
	return set
}

func planSources(t *testing.T, set *policy.Set) Sources {
	t.Helper()
	defaults, err := values.ParseDefaults([]byte(`
aws:
  "1": {Environment: Production}
  types:
    AWS DynamoDB Table: {Environment: Staging}
gcp:
  "*": {Brand: Acme Corp}
`), set)
	if err != nil {
		t.Fatal(err)
	}
	owners, err := values.ParseOwnership([]byte("aws: {\"1\": Platform}\ngcp: {p: ITOps}\n"), set.Dict)
	if err != nil {
		t.Fatal(err)
	}
	syn, err := values.ParseSynonyms([]byte("Environment: {prd: Production}\n"), set.Dict)
	if err != nil {
		t.Fatal(err)
	}
	return Sources{
		Set:       map[string]string{"AdminEmail": "ops@example.com"},
		Defaults:  defaults,
		Ownership: owners,
		Synonyms:  syn,
		Parents: map[ParentKey][]model.Tag{
			{model.AWS, "1", "1"}:          {tag("Brand", "Acme")},
			{model.Azure, "s", "/rg"}:      {tag("brand", "Contoso"), tag("DeploymentType", "Automation")},
			{model.GCP, "p", "projects/p"}: {label("adminemail", "x")},
		},
	}
}

func TestBuildPlan(t *testing.T) {
	set := loadPlanPolicy(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Minute)
	aws := func(id string, tags ...model.Tag) model.Resource {
		r := res("AWS::DynamoDB::Table", id, id, tags...)
		r.Parent = "1"
		return r
	}
	gcp := func(native, id string, tags ...model.Tag) model.Resource {
		return model.Resource{Cloud: model.GCP, Scope: "p", NativeType: native, ID: id, Name: id, Parent: "projects/p", Tags: tags}
	}
	young := aws("t4")
	young.Created = &recent
	in := Input{
		Scopes: []string{"GCP/p", "AWS/1", "Azure/s"},
		Resources: []model.Resource{
			aws("t1"),
			aws("t2", tag("DeploymentType", "Automation"), tag("Environment", "prd"), tag("brand", "  "),
				tag("AdminEmail", "a@example.com"), tag("OwningOrg", "platform")),
			aws("t3", tag("DeploymentType", "Scripted")),
			young,
			aws("t5", tag("DeploymentType", "Manual"), tag("Environment", "qa"), tag("Brand", "B"),
				tag("AdminEmail", "a@example.com"), tag("OwningOrg", "ITOps")),
			gcp("compute.googleapis.com/Instance", "g1", label("environment", "production"), label("deploymenttype", "manual"),
				label("owningorg", "itops"), label("brand", "acme")),
			gcp("secretmanager.googleapis.com/Secret", "g2", label("deploymenttype", "manual")),
			gcp("compute.googleapis.com/InstanceTemplate", "g3", label("environment", "production"), label("owningorg", "itops"),
				label("brand", "acme")),
			{Cloud: model.Azure, Scope: "s", NativeType: "microsoft.compute/virtualmachines", ID: "vm", Name: "vm", Parent: "/rg",
				Tags: []model.Tag{tag("environment", "Staging"), tag("OwningOrg", "ITOps"), tag("AdminEmail", "a@example.com")}},
		},
	}
	res := Evaluate(set, in, Options{})
	p := BuildPlan(set, in, res, planSources(t, set), PlanOptions{Now: now, Grace: 5 * time.Minute})

	type key struct{ id, tag string }
	got := map[key]model.Change{}
	for _, c := range p.Changes {
		got[key{c.ResourceID, c.Tag}] = c
	}
	want := []struct {
		id, tag, new, rule string
		action             model.Action
		route              model.Route
		kind               model.TagKind
	}{
		{"t1", "DeploymentType", "Manual", "builtin:deploymenttype", model.ActionAdd, model.RouteDirect, ""},
		{"t1", "Environment", "Staging", "defaults:type", model.ActionAdd, model.RouteDirect, ""},
		{"t1", "OwningOrg", "Platform", "builtin:ownership", model.ActionAdd, model.RouteDirect, ""},
		{"t1", "AdminEmail", "ops@example.com", "set", model.ActionAdd, model.RouteDirect, ""},
		{"t1", "Brand", "Acme", "parent", model.ActionAdd, model.RouteDirect, ""},
		{"t2", "Environment", "Production", "builtin:synonym", model.ActionUpdate, model.RouteTerraform, ""},
		{"t2", "brand", "Acme", "parent", model.ActionUpdate, model.RouteTerraform, ""},
		{"t2", "OwningOrg", "Platform", "builtin:case", model.ActionUpdate, model.RouteTerraform, ""},
		{"g1", "AdminEmail", "ops@example.com", "set", model.ActionAdd, model.RouteDirect, model.KindRMTag},
		{"g2", "DeploymentType", "Manual", "", "", "", ""}, // compliant: lowercase label
		{"g3", "AdminEmail", "ops@example.com", "set", model.ActionAdd, model.RouteUnwritable, model.KindRMTag},
		{"g3", "DeploymentType", "Manual", "builtin:deploymenttype", model.ActionAdd, model.RouteUnwritable, model.KindLabel},
		{"vm", "DeploymentType", "Manual", "builtin:deploymenttype", model.ActionAdd, model.RouteDirect, ""},
		{"vm", "Brand", "Contoso", "parent", model.ActionAdd, model.RouteDirect, ""},
	}
	for _, w := range want {
		c, ok := got[key{w.id, w.tag}]
		if w.rule == "" {
			if ok {
				t.Errorf("%s/%s: unexpected change %+v", w.id, w.tag, c)
			}
			continue
		}
		if !ok {
			t.Errorf("%s/%s: no change", w.id, w.tag)
			continue
		}
		if c.New != w.new || c.Rule != w.rule || c.Action != w.action || c.Route != w.route || c.Kind != w.kind {
			t.Errorf("%s/%s = %+v, want %+v", w.id, w.tag, c, w)
		}
	}
	if c := got[key{"t2", "Environment"}]; c.Old != "prd" || c.Expect != "prd" {
		t.Errorf("update precondition = %+v", c)
	}
	if c := got[key{"t1", "Brand"}]; c.Expect != "" || c.Old != "" {
		t.Errorf("add precondition = %+v", c)
	}

	reasons := map[key]string{}
	for _, u := range p.Unresolved {
		reasons[key{u.ID, u.Tag}] = u.Reason
	}
	wantReasons := map[key]string{
		{"t3", "DeploymentType"}: ReasonNotFixable,
		{"t3", "Environment"}:    ReasonBadDeployment,
		{"t3", "Brand"}:          ReasonBadDeployment,
		{"t5", "Environment"}:    ReasonNoSynonym,
		{"g2", "Brand"}:          ReasonLimit, // "Acme Corp" is not a valid label value
		{"g1", "Environment"}:    "",
	}
	for k, w := range wantReasons {
		if reasons[k] != w {
			t.Errorf("unresolved %v = %q, want %q", k, reasons[k], w)
		}
	}
	if len(reasons) != 7 { // t3: all five tags; t5 Environment; g2 Brand
		t.Errorf("unresolved = %+v", p.Unresolved)
	}
	if p.Summary.SkippedGrace != 1 || got[key{"t4", "DeploymentType"}].Rule != "" {
		t.Errorf("grace: skipped %d", p.Summary.SkippedGrace)
	}
	if p.Summary.ByRoute[model.RouteUnwritable] != 2 || p.Summary.Changes != len(p.Changes) || p.Summary.Resources != 5 {
		t.Errorf("summary = %+v", p.Summary)
	}
	if p.PolicyHash != set.Hash || p.Scopes[0] != "AWS/1" || p.Grace != "5m0s" {
		t.Errorf("plan header = %q %v %q", p.PolicyHash, p.Scopes, p.Grace)
	}

	// The same state gives the same changes.
	again := BuildPlan(set, in, Evaluate(set, in, Options{}), planSources(t, set), PlanOptions{Now: now, Grace: 5 * time.Minute})
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Error("plan is not deterministic")
	}
}

func TestBuildPlanKeyCase(t *testing.T) {
	set := loadPlanPolicy(t)
	in := Input{Resources: []model.Resource{
		res("AWS::DynamoDB::Table", "t", "t", tag("DeploymentType", "manual"), tag("environment", "Production"),
			tag("OwningOrg", "ITOps"), tag("AdminEmail", "a@example.com"), tag("Brand", "B")),
	}}
	p := BuildPlan(set, in, Evaluate(set, in, Options{}), Sources{}, PlanOptions{})
	if len(p.Changes) != 2 {
		t.Fatalf("changes = %+v", p.Changes)
	}
	// The wrong key spelling gets the right one added beside it.
	dt, env := p.Changes[0], p.Changes[1]
	if dt.Tag != "DeploymentType" || dt.Action != model.ActionUpdate || dt.New != "Manual" || dt.Expect != "manual" {
		t.Errorf("value case = %+v", dt)
	}
	if env.Tag != "Environment" || env.Action != model.ActionAdd || env.New != "Production" || env.Route != model.RouteDirect {
		t.Errorf("key case = %+v", env)
	}
}

func TestParentRequests(t *testing.T) {
	set := loadPlanPolicy(t)
	a := res("AWS::DynamoDB::Table", "a", "a", tag("Brand", "x"), tag("Environment", "Production"),
		tag("OwningOrg", "ITOps"), tag("AdminEmail", "a@example.com")) // only DeploymentType missing
	b := res("AWS::DynamoDB::Table", "b", "b")
	b.Scope = "2"
	in := Input{Resources: []model.Resource{a, b}}
	got := ParentRequests(in, Evaluate(set, in, Options{}))
	if len(got) != 1 || got[ParentKey{model.AWS, "2", ""}].ID != "b" {
		t.Errorf("requests = %+v", got)
	}
}
