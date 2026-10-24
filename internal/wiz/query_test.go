package wiz

import (
	"strings"
	"testing"
)

const valid = `{
  "type": ["SERVICE_CONFIGURATION", "VIRTUAL_MACHINE_IMAGE"],
  "select": true,
  "where": {
    "nativeType": {"EQUALS": ["ami"]},
    "cloudPlatform": {"EQUALS": ["AWS"]},
    "_or": [
      {"tags": {"TAG_DOES_NOT_CONTAIN_ANY": [{"key": "DeploymentType", "value": "Manual"}, {"key": "DeploymentType", "value": "Automation"}]}},
      {"tags": {"TAG_DOES_NOT_CONTAIN_ANY": [{"key": "AdminEmail"}]}}
    ]
  },
  "relationships": [{
    "type": [{"type": "USES"}],
    "negate": true,
    "with": {
      "type": ["SERVICE_CONFIGURATION"],
      "where": {"nativeType": {"EQUALS": ["ec2#amitags"]}, "_or": [{"tags": {"TAG_CONTAINS_ANY": [{"key": "AdminEmail"}]}}]}
    }
  }]
}`

func TestParseValid(t *testing.T) {
	q, issues := Parse([]byte(valid), "missing")
	if len(issues) > 0 {
		t.Fatalf("issues: %v", issues)
	}
	if len(q.Tags) != 4 {
		t.Fatalf("tag conditions = %d, want 4", len(q.Tags))
	}
	keyOnly := 0
	for _, c := range q.Tags {
		if c.Value == nil {
			keyOnly++
		}
	}
	if keyOnly != 2 {
		t.Errorf("key-only conditions = %d, want 2", keyOnly)
	}
}

func TestParseRejects(t *testing.T) {
	tests := map[string]struct{ from, to, wantPath string }{
		"unknown operator":         {`"EQUALS": ["ami"]`, `"STARTS_WITH": ["ami"]`, "missing.where.nativeType.STARTS_WITH"},
		"unknown field":            {`"cloudPlatform"`, `"region"`, "missing.where.region"},
		"unknown top-level key":    {`"select": true,`, `"select": true, "limit": 5,`, "missing.limit"},
		"relationship not negated": {`"negate": true`, `"negate": false`, "missing.relationships[0].negate"},
		"unknown relationship":     {`{"type": "USES"}`, `{"type": "OWNS"}`, "missing.relationships[0].type[0].type"},
		"empty _or":                {`"_or": [{"tags": {"TAG_CONTAINS_ANY": [{"key": "AdminEmail"}]}}]`, `"_or": []`, "missing.relationships[0].with.where._or"},
		"tag without key":          {`[{"key": "AdminEmail"}]}}]`, `[{"value": "x"}]}}]`, "missing.relationships[0].with.where._or[0].tags.TAG_CONTAINS_ANY[0]"},
		"select not bool":          {`"select": true`, `"select": "yes"`, "missing.select"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(valid, tc.from, tc.to, 1)
			if src == valid {
				t.Fatalf("fixture replacement %q did not apply", tc.from)
			}
			_, issues := Parse([]byte(src), "missing")
			for _, is := range issues {
				if is.Path == tc.wantPath {
					return
				}
			}
			t.Fatalf("no issue at %s; got %v", tc.wantPath, issues)
		})
	}
}
