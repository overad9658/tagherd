package engine

import (
	"testing"
	"testing/fstest"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
)

// A small policy: two groups share the DynamoDB table (to exercise the union),
// one filters by name, one has an override.
var testPolicy = fstest.MapFS{
	"tags.json": {Data: []byte(`{"Environment": ["Production", "Staging"], "DeploymentType": ["Manual", "Automation"], "OwningOrg": ["ITOps"], "AdminEmail": null}`)},
	"a.json": {Data: []byte(`{"name": "A", "description": "a", "tags": ["Environment"],
		"target": {"AWS": ["AWS DynamoDB Table", "AWS Lambda Function"], "Azure": [], "GCP": []},
		"filters": {"AWS": {"name": {"DOES_NOT_CONTAIN": ["controltower"]}}}}`)},
	"b.json": {Data: []byte(`{"name": "B", "description": "b", "tags": ["DeploymentType"],
		"target": {"AWS": ["AWS DynamoDB Table"], "Azure": [], "GCP": []}}`)},
	"img.json": {Data: []byte(`{"name": "Images", "description": "i", "tags": ["OwningOrg", "AdminEmail"],
		"target": {"AWS": ["AWS Machine Image (AMI)", "AWS IAM AWS Managed Policy"], "Azure": [], "GCP": []},
		"override": {"AWS": {"all": null, "missing": {"type": ["VIRTUAL_MACHINE_IMAGE"]}}}}`)},
}

func loadTestPolicy(t *testing.T) *policy.Set {
	t.Helper()
	cat, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	set, rep := policy.LoadFS(testPolicy, cat)
	if set == nil {
		t.Fatalf("policy: %v", rep.Diagnostics)
	}
	return set
}

func res(native, id, name string, tags ...model.Tag) model.Resource {
	return model.Resource{Cloud: model.AWS, Scope: "1", NativeType: native, ID: id, Name: name, Tags: tags}
}

func codes(fs []model.Finding) map[string]model.Code {
	out := map[string]model.Code{}
	for _, f := range fs {
		out[f.ID+"/"+f.Tag] = f.Code
	}
	return out
}

func TestUnionAndFilter(t *testing.T) {
	set := loadTestPolicy(t)
	in := Input{Resources: []model.Resource{
		res("AWS::DynamoDB::Table", "t1", "t1"), // needs Environment (A) and DeploymentType (B)
		res("AWS::Lambda::Function", "f1", "aws-controltower-x"),
		res("AWS::Lambda::Function", "f2", "api", tag("Environment", "Production")),
		res("AWS::SQS::Queue", "q1", "q"), // in no group
	}}
	r := Evaluate(set, in, Options{})

	got := codes(r.Findings)
	if got["t1/Environment"] != model.MissingTag || got["t1/DeploymentType"] != model.MissingTag {
		t.Errorf("union not applied: %v", got)
	}
	for _, f := range r.Findings {
		if f.ID == "t1" && f.Group != "A, B" {
			t.Errorf("group = %q, want both groups", f.Group)
		}
	}
	c := r.Summary.Resources
	want := model.Counts{Discovered: 4, OutOfScope: 1, Filtered: 1, Evaluated: 2, Compliant: 1}
	if c != want {
		t.Errorf("counts = %+v, want %+v", c, want)
	}
	byGroup := map[string]model.Rate{}
	for _, g := range r.Summary.Compliance.ByGroup {
		byGroup[g.Key] = g
	}
	if byGroup["A"].Evaluated != 2 || byGroup["A"].Compliant != 1 || byGroup["B"].Evaluated != 1 || byGroup["B"].Percent != 0 {
		t.Errorf("by group = %+v", r.Summary.Compliance.ByGroup)
	}
	if r.Summary.Compliance.Overall.Percent != 50 {
		t.Errorf("overall = %+v", r.Summary.Compliance.Overall)
	}
}

func TestGroupAndTypeSelection(t *testing.T) {
	set := loadTestPolicy(t)
	in := Input{Resources: []model.Resource{res("AWS::DynamoDB::Table", "t1", "t1")}}
	b, _ := set.Select([]string{"B"})

	r := Evaluate(set, in, Options{Groups: b})
	if got := codes(r.Findings); len(got) != 1 || got["t1/DeploymentType"] != model.MissingTag {
		t.Errorf("only B's tags should apply: %v", got)
	}
	r = Evaluate(set, in, Options{Types: []string{"*lambda*"}})
	if r.Summary.Resources.OutOfScope != 1 || len(r.Findings) != 0 {
		t.Errorf("--type should drop the table: %+v", r.Summary.Resources)
	}
}

func TestOverride(t *testing.T) {
	set := loadTestPolicy(t)
	in := Input{Resources: []model.Resource{
		res("AWS::EC2::Image", "ami-1", "a", tag("OwningOrg", "itops")), // reported by Wiz
		res("AWS::EC2::Image", "ami-2", "b", tag("OwningOrg", "ITOps")), // not reported
		res("AWS::IAM::Policy", "p1", "ReadOnly"),                       // not taggable
	}}
	in.Resources[2].Attrs = map[string]string{"policy_owner": "aws"}
	key := OverrideKey{"Images", model.AWS}

	// Override ran: ami-2 is exempt from the missing AdminEmail; the case
	// mismatch on ami-1 is still reported, as overrides only cover missing.
	in.Overrides = map[OverrideKey]*OverrideResult{key: {Missing: map[string]bool{"ami-1": true}}}
	r := Evaluate(set, in, Options{})
	got := codes(r.Findings)
	if got["ami-1/AdminEmail"] != model.MissingTag || got["ami-1/OwningOrg"] != model.CaseMismatch {
		t.Errorf("ami-1: %v", got)
	}
	if _, ok := got["ami-2/AdminEmail"]; ok {
		t.Errorf("ami-2 should be exempt: %v", got)
	}
	if got["p1/"] != model.NotTaggable || r.Summary.Resources.NotTaggable != 1 || r.Summary.Resources.Evaluated != 2 {
		t.Errorf("not taggable: %v %+v", got, r.Summary.Resources)
	}
	if len(r.Summary.OverrideFallback) != 0 {
		t.Errorf("fallback = %v", r.Summary.OverrideFallback)
	}

	// Override did not run: the default test applies and the summary says so.
	in.Overrides = nil
	r = Evaluate(set, in, Options{})
	if codes(r.Findings)["ami-2/AdminEmail"] != model.MissingTag {
		t.Error("fallback should report ami-2's missing AdminEmail")
	}
	if len(r.Summary.OverrideFallback) != 1 || r.Summary.OverrideFallback[0] != "Images (AWS)" {
		t.Errorf("fallback = %v", r.Summary.OverrideFallback)
	}
}

func TestUnmappedAndDeterminism(t *testing.T) {
	set := loadTestPolicy(t)
	in := Input{
		Resources: []model.Resource{
			res("AWS::Lambda::Function", "f2", "b"),
			res("AWS::Lambda::Function", "f1", "a"),
		},
		Unmapped:   []model.CoverageGap{{Cloud: model.AWS, Scope: "1", Type: "AWS Lambda Function"}},
		Unreadable: []model.UnreadableScope{{Cloud: model.AWS, Scope: "2", Error: "denied"}},
	}
	r := Evaluate(set, in, Options{})
	if r.Findings[0].Code != model.UnmappedType || r.Findings[0].Group != "A" {
		t.Errorf("first finding = %+v", r.Findings[0])
	}
	if r.Findings[1].ID != "f1" || r.Findings[2].ID != "f2" {
		t.Errorf("findings not sorted by ID: %+v", r.Findings)
	}
	if r.Summary.ByCode[model.UnmappedType] != 1 || len(r.Summary.Unreadable) != 1 {
		t.Errorf("summary = %+v", r.Summary)
	}
}

func TestUnclassified(t *testing.T) {
	set := loadTestPolicy(t)
	// Every RDS instance entry requires an engine; none matches a missing one.
	r := Evaluate(set, Input{Resources: []model.Resource{res("AWS::RDS::DBInstance", "db", "db")}}, Options{})
	if r.Summary.Resources.OutOfScope != 1 || len(r.Summary.Warnings) != 0 {
		t.Errorf("%+v", r.Summary)
	}
}
