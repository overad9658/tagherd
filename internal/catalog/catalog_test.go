package catalog

import (
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
