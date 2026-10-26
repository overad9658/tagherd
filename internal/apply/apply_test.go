package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
	"github.com/overad9658/tagherd/internal/provider"
	"github.com/overad9658/tagherd/internal/provider/fake"
)

const fixture = `
scopes:
  - cloud: AWS
    id: "1"
    resources:
      - {native_type: AWS::S3::Bucket, id: b1, name: b1, tags: {Environment: production}}
      - {native_type: AWS::S3::Bucket, id: b2, name: b2, tags: {Environment: Staging}}
      - {native_type: AWS::S3::Bucket, id: b3, name: b3, write_fail: Throttling}
  - cloud: AWS
    id: "2"
    writer_fail: "AccessDenied: tagherd-write"
    resources:
      - {native_type: AWS::S3::Bucket, id: c1, name: c1}
  - cloud: GCP
    id: p
    resources:
      - {native_type: compute.googleapis.com/Instance, id: g1, name: g1, labels: {environment: prod}}
`

func change(cloud model.Cloud, scope, id, tag string, action model.Action, expect, value string) model.Change {
	return model.Change{
		ResourceID: id, Cloud: cloud, Scope: scope, Type: "AWS S3 Bucket", NativeType: "AWS::S3::Bucket",
		Tag: tag, Action: action, Old: expect, Expect: expect, New: value, Rule: "set", Route: model.RouteDirect,
	}
}

func setupRun(t *testing.T) (*fake.Env, map[model.Cloud]provider.Provider) {
	t.Helper()
	env, err := fake.Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	ps := map[model.Cloud]provider.Provider{}
	for c, p := range env.Providers {
		ps[c] = p
	}
	return env, ps
}

func TestRun(t *testing.T) {
	env, ps := setupRun(t)
	gcp := change(model.GCP, "p", "g1", "environment", model.ActionUpdate, "prod", "Production")
	gcp.Kind, gcp.Type, gcp.NativeType = model.KindLabel, "GCP Compute Instance", "compute.googleapis.com/Instance"
	adminEmail := change(model.GCP, "p", "g1", "AdminEmail", model.ActionAdd, "", "ops@example.com")
	adminEmail.Kind = model.KindRMTag
	tf := change(model.AWS, "1", "b1", "Brand", model.ActionAdd, "", "Acme")
	tf.Route = model.RouteTerraform
	plan := model.Plan{ID: "P1", Changes: []model.Change{
		change(model.AWS, "1", "b1", "Environment", model.ActionUpdate, "production", "Production"), // written
		change(model.AWS, "1", "b1", "DeploymentType", model.ActionAdd, "", "Manual"),               // written, same call
		change(model.AWS, "1", "b2", "Environment", model.ActionUpdate, "production", "Production"), // stale: now Staging
		change(model.AWS, "1", "b2", "Owner", model.ActionAdd, "", "x"),                             // written
		change(model.AWS, "1", "b3", "DeploymentType", model.ActionAdd, "", "Manual"),               // write fails
		change(model.AWS, "1", "nope", "DeploymentType", model.ActionAdd, "", "Manual"),             // not found
		change(model.AWS, "2", "c1", "DeploymentType", model.ActionAdd, "", "Manual"),               // no write role
		gcp, adminEmail, tf,
	}}
	var audit bytes.Buffer
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	rep := Run(context.Background(), plan, Options{Providers: ps, Audit: &audit, Concurrency: 2, Now: func() time.Time { return now }})

	want := []model.Outcome{
		model.OutcomeWritten, model.OutcomeWritten, model.OutcomeStale, model.OutcomeWritten, model.OutcomeFailed,
		model.OutcomeFailed, model.OutcomeFailed, model.OutcomeWritten, model.OutcomeWritten, model.OutcomePending,
	}
	for i, w := range want {
		if got := rep.Results[i]; got.Outcome != w {
			t.Errorf("%d %s/%s = %s (%s), want %s", i, got.ResourceID, got.Tag, got.Outcome, got.Detail, w)
		}
	}
	if !strings.Contains(rep.Results[2].Detail, `found "Staging"`) || !strings.Contains(rep.Results[6].Detail, "AccessDenied") {
		t.Errorf("details = %q, %q", rep.Results[2].Detail, rep.Results[6].Detail)
	}
	if rep.Summary != (model.ApplySummary{Written: 5, Stale: 1, Failed: 3, Pending: 1}) {
		t.Errorf("summary = %+v", rep.Summary)
	}

	// One write call per resource, lowercase GCP labels, RM tags as given.
	writes := env.Providers[model.GCP].Writes()
	if len(writes) != 1 || writes[0].Tags[0] != (model.Tag{Key: "environment", Value: "production", Kind: model.KindLabel}) ||
		writes[0].Tags[1] != (model.Tag{Key: "AdminEmail", Value: "ops@example.com", Kind: model.KindRMTag}) {
		t.Errorf("gcp writes = %+v", writes)
	}
	if n := len(env.Providers[model.AWS].Writes()); n != 2 {
		t.Errorf("aws write calls = %d, want b1 and b2", n)
	}

	// Every attempt is audited; the pending Terraform change is not.
	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	if len(lines) != 9 {
		t.Fatalf("audit lines = %d:\n%s", len(lines), audit.String())
	}
	var rec model.AuditRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.PlanID != "P1" || rec.Actor == "" || !rec.Time.Equal(now) {
		t.Errorf("audit = %+v", rec)
	}

	// A second apply of the same plan changes nothing.
	again := Run(context.Background(), plan, Options{Providers: ps})
	if again.Summary.Written != 0 || again.Summary.Noop != 5 {
		t.Errorf("second run = %+v", again.Summary)
	}
}

type stubTerraform struct{ outcomes []model.Outcome }

func (s stubTerraform) Run(_ context.Context, _ string, cs []model.Change) []model.ChangeResult {
	out := make([]model.ChangeResult, len(cs))
	for i, c := range cs {
		out[i] = model.ChangeResult{Change: c, Outcome: s.outcomes[i], Detail: string(s.outcomes[i]),
			Terraform: &model.TerraformTarget{Address: "aws_s3_bucket." + c.ResourceID}}
	}
	return out
}

func (stubTerraform) Actor() string { return "local:test" }

func TestRunTerraform(t *testing.T) {
	var cs []model.Change
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		c := change(model.AWS, "1", id, "Brand", model.ActionAdd, "", "Acme")
		c.Route = model.RouteTerraform
		cs = append(cs, c)
	}
	var audit bytes.Buffer
	stub := stubTerraform{[]model.Outcome{model.OutcomeProposed, model.OutcomeNoop, model.OutcomeUnroutable, model.OutcomeFailed}}
	rep := Run(context.Background(), model.Plan{ID: "P", Changes: cs}, Options{Terraform: stub, Audit: &audit})
	if rep.Summary != (model.ApplySummary{Proposed: 1, Noop: 1, Unroutable: 1, Failed: 1}) {
		t.Errorf("summary = %+v", rep.Summary)
	}
	if rep.Results[0].Terraform == nil || rep.Results[0].Terraform.Address != "aws_s3_bucket.t1" {
		t.Errorf("result = %+v", rep.Results[0])
	}
	// Edits in code are attempts; unroutable changes are not.
	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	var rec model.AuditRecord
	_ = json.Unmarshal([]byte(lines[0]), &rec)
	if len(lines) != 3 || rec.Actor != "local:test" || rec.Route != model.RouteTerraform || rec.Outcome != model.OutcomeProposed {
		t.Errorf("audit:\n%s", audit.String())
	}
}

func TestCheck(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	p := model.Plan{PolicyHash: "abc", Created: now.Add(-25 * time.Hour)}
	if err := Check(p, "abc", now, false); !errors.Is(err, ErrPlanTooOld) {
		t.Errorf("old plan: %v", err)
	}
	if err := Check(p, "abc", now, true); err != nil {
		t.Errorf("--allow-stale: %v", err)
	}
	if err := Check(p, "def", now, true); !errors.Is(err, ErrPolicyChanged) {
		t.Errorf("hash: %v", err)
	}
}

func TestLimits(t *testing.T) {
	tag := func(k, v string) model.Tag { return model.Tag{Key: k, Value: v, Kind: model.KindTag} }
	many := func(n int) []model.Tag {
		var out []model.Tag
		for i := range n {
			out = append(out, tag(strings.Repeat("k", i+1), "v"))
		}
		return out
	}
	tests := []struct {
		name     string
		cloud    model.Cloud
		existing []model.Tag
		writes   []model.Tag
		want     string
	}{
		{"aws reserved", model.AWS, nil, []model.Tag{tag("AWS:x", "v")}, "reserved"},
		{"aws count", model.AWS, many(50), []model.Tag{tag("new", "v")}, "51 tags"},
		{"aws: tags do not count", model.AWS, append(many(49), tag("aws:cloudformation:stack-id", "x")), []model.Tag{tag("new", "v")}, ""},
		{"aws value", model.AWS, nil, []model.Tag{tag("k", strings.Repeat("v", 257))}, "256"},
		{"azure count ignores key case", model.Azure, many(50), []model.Tag{tag("K", "v")}, ""},
		{"gcp label value", model.GCP, nil, []model.Tag{{Key: "brand", Value: "acme corp", Kind: model.KindLabel}}, "not a valid value"},
		{"gcp rm tag", model.GCP, nil, []model.Tag{{Key: "AdminEmail", Value: "ops@example.com", Kind: model.KindRMTag}}, ""},
	}
	for _, tc := range tests {
		err := checkLimits(tc.cloud, tc.existing, tc.writes)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	// A limit failure stops the whole resource.
	_, ps := setupRun(t)
	p := model.Plan{Changes: []model.Change{
		change(model.AWS, "1", "b1", "aws:owner", model.ActionAdd, "", "x"),
		change(model.AWS, "1", "b1", "Brand", model.ActionAdd, "", "Acme"),
	}}
	rep := Run(context.Background(), p, Options{Providers: ps})
	if rep.Summary.Failed != 2 {
		t.Errorf("summary = %+v", rep.Summary)
	}
}

func TestVerify(t *testing.T) {
	_, ps := setupRun(t)
	cat, _ := catalog.Default()
	set, rep := policy.Load("../../policy", cat)
	if set == nil {
		t.Fatal(rep.Diagnostics)
	}
	p := model.Plan{Changes: []model.Change{change(model.AWS, "1", "b1", "Environment", model.ActionUpdate, "production", "Production")}}
	res := Run(context.Background(), p, Options{Providers: ps})
	findings, warnings := Verify(context.Background(), set, res, Options{Providers: ps})
	if len(warnings) != 0 || len(findings) == 0 {
		t.Fatalf("findings = %v, warnings = %v", findings, warnings)
	}
	for _, f := range findings {
		if f.ID != "b1" || f.Tag == "Environment" {
			t.Errorf("unexpected finding %+v", f)
		}
	}
}
