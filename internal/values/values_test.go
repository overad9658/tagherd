package values

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
)

func realSet(t *testing.T) *policy.Set {
	t.Helper()
	cat, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	set, rep := policy.Load("../../policy", cat)
	if set == nil {
		t.Fatalf("policy: %v", rep.Diagnostics)
	}
	return set
}

func TestDefaults(t *testing.T) {
	set := realSet(t)
	d, err := ParseDefaults([]byte(`
AWS:
  "*": {Exposure: internal}
  111122223333:              # unquoted IDs are fine
    Brand: Acme
    environment: Staging
  groups:
    database: {DataClassification: Confidential}
  types:
    aws s3 bucket: {Environment: Production}
azure:
  "*": {Exposure: External}
`), set)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		cloud           model.Cloud
		scope, typ, tag string
		groups          []string
		value, level    string
	}{
		{model.AWS, "111122223333", "AWS S3 Bucket", "Environment", nil, "Production", LevelType},
		{model.AWS, "111122223333", "AWS DynamoDB Table", "Environment", nil, "Staging", LevelScope},
		{model.AWS, "111122223333", "AWS DynamoDB Table", "DataClassification", []string{"Database"}, "Confidential", LevelGroup},
		{model.AWS, "999", "AWS DynamoDB Table", "Exposure", nil, "Internal", LevelCloud},
		{model.AWS, "999", "AWS DynamoDB Table", "Brand", nil, "", ""},
		{model.Azure, "s", "x", "Exposure", nil, "External", LevelCloud},
		{model.GCP, "p", "x", "Exposure", nil, "", ""},
	}
	for _, tc := range tests {
		v, level, ok := d.Lookup(tc.cloud, tc.scope, tc.typ, tc.groups, tc.tag)
		if v != tc.value || level != tc.level || ok != (tc.value != "") {
			t.Errorf("%s %s %s %s = %q %q %v, want %q %q", tc.cloud, tc.scope, tc.typ, tc.tag, v, level, ok, tc.value, tc.level)
		}
	}
}

func TestDefaultsErrors(t *testing.T) {
	set := realSet(t)
	for _, tc := range []struct{ yaml, want string }{
		{"oracle: {}", "unknown cloud"},
		{"aws: {'1': {Colour: Red}}", "Colour is not a tag"},
		{"aws: {'1': {Environment: Prod}}", `Environment="Prod" is not a valid value (want one of Production`},
		{"aws: {'1': {AdminEmail: nobody}}", "want an email address"},
		{"aws: {groups: {Nope: {Brand: A}}}", "Nope is not a policy group"},
		{"aws: {types: {AWS Nope: {Brand: A}}}", "AWS Nope is not a AWS type"},
		{"aws: {'1': {Brand: [a, b]}}", "must be a single value"},
		{"aws: {'1': {Brand: A, brand: B}}", "Brand appears twice"},
		{"- a", "must be a mapping"},
	} {
		_, err := ParseDefaults([]byte(tc.yaml), set)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.yaml, err, tc.want)
		}
	}
}

func TestOwnershipAndSynonyms(t *testing.T) {
	set := realSet(t)
	o, err := ParseOwnership([]byte("aws: {\"111122223333\": platform}\ngcp: {p: DevOps}\n"), set.Dict)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := o.Lookup(model.AWS, "111122223333"); !ok || v != "Platform" {
		t.Errorf("ownership = %q %v", v, ok)
	}
	if _, err := ParseOwnership([]byte("aws: {\"1\": Legal}\n"), set.Dict); err == nil || !strings.Contains(err.Error(), "OwningOrg=\"Legal\"") {
		t.Errorf("bad owner: %v", err)
	}

	s, err := ParseSynonyms([]byte("environment: {PROD: production, dev: Development}\nOwningOrg: {it-ops: ITOps}\n"), set.Dict)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := s.Lookup("Environment", " Prod "); !ok || v != "Production" {
		t.Errorf("synonym = %q %v", v, ok)
	}
	if _, ok := s.Lookup("Environment", "qa"); ok {
		t.Error("unknown value matched")
	}
	for _, tc := range []struct{ yaml, want string }{
		{"DeploymentType: {man: Manual}", "never corrected"},
		{"Environment: {prod: Prod}", "not a valid value"},
		{"Environment: {prod: Production, PROD: Production}", "ignore case"},
		{"Nope: {a: b}", "Nope is not a tag"},
	} {
		if _, err := ParseSynonyms([]byte(tc.yaml), set.Dict); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.yaml, err, tc.want)
		}
	}
}

func TestParseSet(t *testing.T) {
	set := realSet(t)
	got, err := ParseSet([]string{"environment=production", "Brand=Acme Corp", "AdminEmail=ops@example.com"}, set.Dict)
	if err != nil || got["Environment"] != "Production" || got["Brand"] != "Acme Corp" {
		t.Errorf("set = %v, %v", got, err)
	}
	for _, f := range [][]string{{"Brand"}, {"Brand="}, {"Nope=x"}, {"Brand=a", "brand=b"}, {"Exposure=Secret"}} {
		if _, err := ParseSet(f, set.Dict); err == nil {
			t.Errorf("%v: no error", f)
		}
	}
}

// The init templates are all comments, so they load as empty.
func TestTemplatesLoad(t *testing.T) {
	set := realSet(t)
	dir := "../cli/templates"
	if _, err := LoadDefaults(filepath.Join(dir, "defaults.yaml"), set); err != nil {
		t.Error(err)
	}
	if _, err := LoadOwnership(filepath.Join(dir, "ownership.yaml"), set.Dict); err != nil {
		t.Error(err)
	}
	if _, err := LoadSynonyms(filepath.Join(dir, "synonyms.yaml"), set.Dict); err != nil {
		t.Error(err)
	}
	if d, err := LoadDefaults("", set); err != nil || d == nil {
		t.Errorf("no file: %v", err)
	}
	if _, err := LoadSynonyms(filepath.Join(t.TempDir(), "missing.yaml"), set.Dict); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}
