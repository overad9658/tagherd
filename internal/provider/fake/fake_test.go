package fake

import (
	"context"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

const fixture = `
scopes:
  - cloud: AWS
    id: "111"
    name: prod
    tags: {Brand: Acme}
    resources:
      - {native_type: "AWS::S3::Bucket", id: b1, name: logs, tags: {Environment: Production}}
      - {native_type: "AWS::SQS::Queue", id: q1, name: jobs}
  - cloud: AWS
    id: "222"
    name: dev
    fail: AccessDenied
`

func TestDiscoverAndWrite(t *testing.T) {
	env, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	p := env.Providers[model.AWS]
	ctx := context.Background()

	scopes, _ := p.Scopes(ctx, []string{"pro*"})
	if len(scopes) != 1 || scopes[0].ID != "111" {
		t.Fatalf("scopes = %+v", scopes)
	}
	cat, _ := catalog.Default()
	bucket, _ := cat.Lookup(model.AWS, "AWS S3 Bucket")
	d, err := p.Discover(ctx, scopes[0], []*catalog.Entry{bucket})
	if err != nil || len(d.Resources) != 1 || d.Resources[0].ID != "b1" {
		t.Fatalf("discover = %+v, %v", d, err)
	}
	if _, err := p.Discover(ctx, provider.Scope{Cloud: model.AWS, ID: "222"}, nil); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("failing scope: err = %v", err)
	}
	parent, _ := p.ParentTags(ctx, d.Resources[0])
	if len(parent) != 1 || parent[0].Value != "Acme" {
		t.Errorf("parent tags = %+v", parent)
	}

	w, _ := p.Writer(ctx, scopes[0])
	if err := w.Write(ctx, "b1", []model.Tag{{Key: "DeploymentType", Value: "Manual"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := w.Read(ctx, "b1", []string{"deploymenttype", "Environment", "Owner"})
	if got["deploymenttype"] != "Manual" || got["Environment"] != "Production" || len(got) != 2 {
		t.Errorf("read = %v", got)
	}
	if ws := p.Writes(); len(ws) != 1 || ws[0].ID != "b1" {
		t.Errorf("writes = %+v", ws)
	}
	if err := w.Write(ctx, "nope", nil); err == nil {
		t.Error("unknown resource should fail")
	}
}

func TestParseRejects(t *testing.T) {
	for _, src := range []string{
		"scopes: [{cloud: OCI, id: x}]",
		"scopes: [{cloud: AWS, id: x, colour: red}]",
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%q should fail", src)
		}
	}
}
