package catalog

import (
	"errors"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/model"
)

func TestDefaultLoads(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[model.Cloud]int{}
	for _, e := range c.Entries {
		counts[e.Cloud]++
	}
	// 265 policy names, less the misplaced "Azure Storage Account" under AWS,
	// plus "AWS Backup Plan" and the Azure "Azure Storage Account".
	want := map[model.Cloud]int{model.AWS: 147, model.Azure: 81, model.GCP: 38}
	for cl, n := range want {
		if counts[cl] != n {
			t.Errorf("%s entries = %d, want %d", cl, counts[cl], n)
		}
	}
}

func TestLookupAndAlias(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := c.Lookup(model.AWS, "aws cloudwatch metric filter alarm")
	if !ok {
		t.Fatal("lookup should ignore case")
	}
	if got := c.Canonical(e).Display; got != "AWS CloudWatch Alarm" {
		t.Errorf("canonical = %q", got)
	}
	if _, ok := c.Lookup(model.Azure, "AWS S3 Bucket"); ok {
		t.Error("lookup must be per cloud")
	}
}

func TestParseRejects(t *testing.T) {
	tests := map[string]struct{ yaml, want string }{
		"missing taggable": {`- {display: "AWS X", cloud: AWS, native_type: "AWS::X"}`, "taggable is required"},
		"bad taggable":     {`- {display: "AWS X", cloud: AWS, native_type: "AWS::X", taggable: maybe}`, "taggable must be"},
		"wrong prefix":     {`- {display: "Azure X", cloud: AWS, native_type: "AWS::X", taggable: true}`, "must start with"},
		"unknown cloud":    {`- {display: "OCI X", cloud: OCI, native_type: "X", taggable: true}`, "cloud must be"},
		"no native type":   {`- {display: "AWS X", cloud: AWS, taggable: true}`, "native_type is required"},
		"unknown field":    {`- {display: "AWS X", cloud: AWS, native_type: "AWS::X", taggable: true, colour: red}`, "colour"},
		"dangling alias":   {`- {display: "AWS X", cloud: AWS, alias_of: "AWS Y", taggable: true}`, "not in the catalog"},
		"alias with type":  {"- {display: \"AWS X\", cloud: AWS, native_type: \"AWS::X\", taggable: true}\n- {display: \"AWS Y\", cloud: AWS, alias_of: \"AWS X\", native_type: \"AWS::Y\", taggable: true}", "takes no native_type"},
		"duplicate":        {"- {display: \"AWS X\", cloud: AWS, native_type: \"AWS::X\", taggable: true}\n- {display: \"aws x\", cloud: AWS, native_type: \"AWS::X2\", taggable: true}", "duplicate entry"},
		"shared, no match": {"- {display: \"AWS X\", cloud: AWS, native_type: \"AWS::X\", taggable: true}\n- {display: \"AWS Y\", cloud: AWS, native_type: \"AWS::X\", match: {a: b}, taggable: true}", "needs a match"},
		"empty matcher":    {`- {display: "AWS X", cloud: AWS, native_type: "AWS::X", match: {a: {}}, taggable: true}`, "empty matcher"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestMatcher(t *testing.T) {
	tests := []struct {
		m     Matcher
		value string
		want  bool
	}{
		{Matcher{Equals: []string{"aurora-mysql", "aurora"}}, "Aurora", true},
		{Matcher{Equals: []string{"aurora-mysql"}}, "mysql", false},
		{Matcher{Contains: "sqlserver"}, "sqlserver-ee", true},
		{Matcher{Contains: "functionapp", NotContains: "workflowapp"}, "functionapp,linux", true},
		{Matcher{Contains: "functionapp", NotContains: "workflowapp"}, "functionapp,workflowapp", false},
		{Matcher{NotContains: "azurefrontdoor"}, "Standard_AzureFrontDoor", false},
	}
	for _, tc := range tests {
		if got := tc.m.Match(tc.value); got != tc.want {
			t.Errorf("%+v.Match(%q) = %v, want %v", tc.m, tc.value, got, tc.want)
		}
	}
}

func TestClassify(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		cloud  model.Cloud
		native string
		attrs  map[string]string
		want   string // "" for no match
	}{
		{model.AWS, "AWS::RDS::DBInstance", map[string]string{"engine": "postgres", "cluster_member": "false"}, "AWS RDS PostgreSQL Instance"},
		{model.AWS, "AWS::RDS::DBInstance", map[string]string{"engine": "postgres", "cluster_member": "true"}, "AWS RDS Cluster PostgreSQL Instance"},
		{model.AWS, "AWS::RDS::DBInstance", map[string]string{"engine": "aurora", "cluster_member": "true"}, "AWS RDS Aurora MySQL Instance"},
		{model.AWS, "AWS::RDS::DBInstance", map[string]string{"engine": "sqlserver-ee"}, "AWS RDS MSSQL Server Instance"},
		{model.AWS, "AWS::RDS::DBInstance", map[string]string{"engine": "db2-se"}, ""},
		{model.AWS, "aws::s3::bucket", nil, "AWS S3 Bucket"},
		{model.AWS, "AWS::ElasticLoadBalancingV2::LoadBalancer", map[string]string{"type": "gateway"}, ""},
		{model.Azure, "microsoft.web/sites", map[string]string{"kind": "functionapp,linux"}, "Azure Function"},
		{model.Azure, "microsoft.web/sites", map[string]string{"kind": "app,linux"}, "Azure App Service"},
		{model.Azure, "microsoft.web/sites", map[string]string{"kind": "functionapp,workflowapp"}, ""},
		{model.Azure, "microsoft.cdn/profiles", map[string]string{"sku": "Premium_AzureFrontDoor"}, "Azure Front Door Standard/Premium"},
		{model.GCP, "compute.googleapis.com/GlobalAddress", nil, "GCP Compute Address"},
		{model.GCP, "AWS::S3::Bucket", nil, ""}, // native types are per cloud
	}
	for _, tc := range tests {
		e, err := c.Classify(tc.cloud, tc.native, tc.attrs)
		if err != nil {
			t.Errorf("%s %v: %v", tc.native, tc.attrs, err)
			continue
		}
		got := ""
		if e != nil {
			got = e.Display
		}
		if got != tc.want {
			t.Errorf("Classify(%s, %v) = %q, want %q", tc.native, tc.attrs, got, tc.want)
		}
	}
}

func TestClassifyAmbiguous(t *testing.T) {
	c, err := Parse([]byte(`
- {display: "AWS A", cloud: AWS, native_type: "AWS::X", match: {kind: {contains: a}}, taggable: true}
- {display: "AWS B", cloud: AWS, native_type: "AWS::X", match: {kind: {contains: b}}, taggable: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Classify(model.AWS, "AWS::X", map[string]string{"kind": "ab"}); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("err = %v, want ErrAmbiguous", err)
	}
}

func TestDiscovery(t *testing.T) {
	for _, tc := range []struct{ discovery, mode, adapter string }{
		{"", DiscoverBulk, ""}, {"bulk", DiscoverBulk, ""}, {"none", DiscoverNone, ""}, {"list:ec2", DiscoverList, "ec2"},
	} {
		e := &Entry{Discovery: tc.discovery}
		if mode, adapter := e.DiscoveryMode(); mode != tc.mode || adapter != tc.adapter {
			t.Errorf("%q: got %s %s", tc.discovery, mode, adapter)
		}
	}
	for _, src := range []string{
		`[{display: "AWS X", cloud: AWS, native_type: "AWS::X::Y", taggable: true, discovery: config}]`,
		`[{display: "AWS X", cloud: AWS, native_type: "AWS::X::Y", taggable: true, discovery: "list:"}]`,
		`[{display: "AWS X", cloud: AWS, native_type: "AWS::X::Y", taggable: true}, {display: "AWS Z", cloud: AWS, alias_of: "AWS X", taggable: true, discovery: none}]`,
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s should fail", src)
		}
	}
}
