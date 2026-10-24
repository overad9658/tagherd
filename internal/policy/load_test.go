package policy

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
)

func defaultCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestRealPolicy is the phase 1 gate: the real files load and report exactly
// the known issues.
func TestRealPolicy(t *testing.T) {
	set, rep := Load("../../policy", defaultCatalog(t))
	if set == nil {
		t.Fatalf("policy did not load: %v", rep.Diagnostics)
	}
	if len(set.Groups) != 17 {
		t.Errorf("groups = %d, want 17", len(set.Groups))
	}

	// Listed entries before dedup must match the spec: 147 / 80 / 38.
	listed := map[model.Cloud]int{}
	for _, d := range rep.Diagnostics {
		if strings.Contains(d.Message, "same type as") || strings.Contains(d.Message, "named for another cloud") {
			c, _ := model.ParseCloud(strings.SplitN(strings.TrimPrefix(d.Path, "target."), "[", 2)[0])
			listed[c]++
		}
	}
	for _, g := range set.Groups {
		for c, ts := range g.Targets {
			listed[c] += len(ts)
		}
	}
	want := map[model.Cloud]int{model.AWS: 147, model.Azure: 80, model.GCP: 38}
	for c, n := range want {
		if listed[c] != n {
			t.Errorf("%s types listed = %d, want %d", c, listed[c], n)
		}
	}

	wantWarnings := []string{
		`machine_learning.json: target.GCP[7]: "GCP Vertex AI User Managed Workbench" is the same type as "GCP Vertex AI Workbench User Managed Notebook"`,
		`machine_learning.json: target.GCP[8]: "GCP Vertex AI Managed Workbench" is the same type as "GCP Vertex AI Workbench Managed Notebook"`,
		`management_and_governance.json: target.AWS[5]: "AWS CloudWatch Metric Filter Alarm" is the same type as "AWS CloudWatch Alarm"`,
		`security_identity_and_compliance.json: target.GCP[2]: "GCP Secret" is the same type as "GCP Secret Manager Secret"`,
		`security_identity_and_compliance.json: target.GCP[4]: "GCP KMS Key" is the same type as "GCP Cloud KMS Crypto Key"`,
		`storage.json: target.AWS[6]: "Azure Storage Account" is listed under AWS`,
	}
	var got []string
	for _, d := range rep.Diagnostics {
		if d.Level == LevelWarning {
			got = append(got, d.String())
		}
	}
	if len(got) != len(wantWarnings) {
		t.Fatalf("warnings = %d, want %d:\n%s", len(got), len(wantWarnings), strings.Join(got, "\n"))
	}
	for i, w := range wantWarnings {
		if !strings.Contains(got[i], w) {
			t.Errorf("warning %d = %q, want it to contain %q", i, got[i], w)
		}
	}

	ami := set.ByType[TypeKey{model.AWS, "AWS Machine Image (AMI)"}]
	if len(ami) != 1 || ami[0].Overrides[model.AWS] == nil {
		t.Error("AMI group should carry the AWS override")
	}
	if f := set.ByType[TypeKey{model.AWS, "AWS Lambda Function"}][0].Filters[model.AWS]; !f.Excludes("aws-controltower-NotificationForwarder") {
		t.Error("Control Tower filter should exclude the forwarder")
	}
}

func TestHashIgnoresFormatting(t *testing.T) {
	a := validFS()
	b := validFS()
	b["tags.json"] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(string(a["tags.json"].Data), "\n", "\n\n   "))}
	sa, _ := LoadFS(a, defaultCatalog(t))
	sb, _ := LoadFS(b, defaultCatalog(t))
	if sa == nil || sb == nil {
		t.Fatal("fixture failed to load")
	}
	if sa.Hash != sb.Hash {
		t.Error("whitespace changed the policy hash")
	}
	c := validFS()
	c["db.json"] = &fstest.MapFile{Data: []byte(strings.Replace(string(c["db.json"].Data), `"Database"`, `"Databases"`, 1))}
	if sc, _ := LoadFS(c, defaultCatalog(t)); sc.Hash == sa.Hash {
		t.Error("a content change kept the same hash")
	}
}

const fixtureTags = `{"Environment": ["Production", "Staging"], "DeploymentType": ["Manual", "Automation"], "OwningOrg": ["ITOps", "DevOps"], "AdminEmail": null}`

const fixtureGroup = `{
  "name": "Database",
  "description": "Databases",
  "tags": ["Environment", "DeploymentType"],
  "target": {"AWS": ["AWS DynamoDB Table"], "Azure": ["Azure SQL Server"], "GCP": []},
  "filters": {"AWS": {"name": {"DOES_NOT_CONTAIN": ["controltower"]}}}
}`

const fixtureAMI = `{
  "name": "Images",
  "description": "Images",
  "tags": ["DeploymentType", "OwningOrg", "AdminEmail"],
  "target": {"AWS": ["AWS Machine Image (AMI)"], "Azure": [], "GCP": []},
  "override": {"AWS": {"all": null, "missing": {
    "type": ["VIRTUAL_MACHINE_IMAGE"],
    "where": {"_or": [
      {"tags": {"TAG_DOES_NOT_CONTAIN_ANY": [{"key": "DeploymentType", "value": "Manual"}, {"key": "DeploymentType", "value": "Automation"}]}},
      {"tags": {"TAG_DOES_NOT_CONTAIN_ANY": [{"key": "OwningOrg", "value": "ITOps"}, {"key": "OwningOrg", "value": "DevOps"}]}}
    ]}
  }}}
}`

func validFS() fstest.MapFS {
	return fstest.MapFS{
		"tags.json": {Data: []byte(fixtureTags)},
		"db.json":   {Data: []byte(fixtureGroup)},
		"ami.json":  {Data: []byte(fixtureAMI)},
	}
}

func TestFixtureIsClean(t *testing.T) {
	set, rep := LoadFS(validFS(), defaultCatalog(t))
	if set == nil || rep.Count(LevelWarning) > 0 || rep.Count(LevelInfo) > 0 {
		t.Fatalf("fixture should be clean: %v", rep.Diagnostics)
	}
}

// TestChecks breaks the valid fixture one way per case and expects exactly
// that check to fire.
func TestChecks(t *testing.T) {
	tests := []struct {
		name, file, from, to string
		level                Level
		want                 string // substring of "file: path: message"
	}{
		{"unknown key", "db.json", `"name":`, `"colour": "red", "name":`, LevelError, `unknown field "colour"`},
		{"missing name", "db.json", `"name": "Database",`, ``, LevelError, "db.json: name: name is required"},
		{"empty tags", "db.json", `["Environment", "DeploymentType"]`, `[]`, LevelError, "at least one required tag"},
		{"unknown tag", "db.json", `"Environment", "DeploymentType"`, `"Environment", "Owner"`, LevelError, `tags[1]: tag "Owner" is not in tags.json`},
		{"tag case ignored", "db.json", `"Environment", "DeploymentType"`, `"environment", "DeploymentType"`, "", ""},
		{"duplicate tag", "db.json", `"Environment", "DeploymentType"`, `"Environment", "ENVIRONMENT"`, LevelWarning, "listed twice"},
		{"missing target", "db.json", `"target"`, `"targets"`, LevelError, `unknown field "targets"`},
		{"missing cloud", "db.json", `, "GCP": []`, ``, LevelError, "target.GCP: missing"},
		{"unknown cloud", "db.json", `"GCP": []`, `"GCP": [], "OCI": []`, LevelError, `target.OCI: unknown cloud`},
		{"wrong prefix", "db.json", `"AWS": ["AWS DynamoDB Table"]`, `"AWS": ["AWS DynamoDB Table", "Azure Storage Account"]`, LevelWarning, "target.AWS[1]: \"Azure Storage Account\" is listed under AWS"},
		{"not in catalog", "db.json", `"AWS DynamoDB Table"`, `"AWS Dynamo Table"`, LevelError, "is not in the type catalog"},
		{"duplicate type", "db.json", `["AWS DynamoDB Table"]`, `["AWS DynamoDB Table", "AWS DynamoDB Table"]`, LevelWarning, "is listed twice"},
		{"alias duplicate", "db.json", `["AWS DynamoDB Table"]`, `["AWS CloudWatch Alarm", "AWS CloudWatch Metric Filter Alarm"]`, LevelWarning, "is the same type as"},
		{"null description", "db.json", `"Databases"`, `null`, LevelInfo, "description is empty"},
		{"empty filter", "db.json", `["controltower"]`, `[]`, LevelError, "needs name.DOES_NOT_CONTAIN"},
		{"filter without targets", "db.json", `"AWS": ["AWS DynamoDB Table"]`, `"AWS": []`, LevelWarning, "filter has no effect"},
		{"override all set", "ami.json", `"all": null`, `"all": {"type": ["X"]}`, LevelError, "override.AWS.all"},
		{"override missing null", "ami.json", "", `{"name": "Images", "description": "x", "tags": ["DeploymentType"], "target": {"AWS": ["AWS Machine Image (AMI)"], "Azure": [], "GCP": []}, "override": {"AWS": {"all": null, "missing": null}}}`, LevelError, "override.AWS.missing: missing is required"},
		{"override bad operator", "ami.json", `TAG_DOES_NOT_CONTAIN_ANY": [{"key": "OwningOrg"`, `TAG_STARTS_WITH": [{"key": "OwningOrg"`, LevelError, "unsupported key \"TAG_STARTS_WITH\""},
		{"override value drift", "ami.json", `"value": "DevOps"`, `"value": "Devops"`, LevelWarning, `value "Devops" for OwningOrg is not an allowed value`},
		{"override missing value", "ami.json", `, {"key": "OwningOrg", "value": "DevOps"}`, ``, LevelWarning, "OwningOrg values do not match tags.json: missing DevOps"},
		{"override unknown tag", "ami.json", `{"key": "DeploymentType", "value": "Manual"}, `, `{"key": "Owner", "value": "x"}, `, LevelWarning, `tag "Owner" is not in tags.json`},
		{"duplicate group name", "ami.json", `"name": "Images"`, `"name": "database"`, LevelError, "is already used by"},
		{"type in two groups", "ami.json", `"AWS": ["AWS Machine Image (AMI)"]`, `"AWS": ["AWS Machine Image (AMI)", "AWS DynamoDB Table"]`, LevelWarning, "the required tags are the union"},
		{"bad dictionary value", "tags.json", `"AdminEmail": null`, `"AdminEmail": "any"`, LevelError, "must be null or a list"},
		{"empty allowed list", "tags.json", `["ITOps", "DevOps"]`, `[]`, LevelError, "must be null (any value) or a non-empty list"},
		{"duplicate dictionary key", "tags.json", `"AdminEmail": null`, `"AdminEmail": null, "adminemail": null`, LevelError, "tag names ignore case"},
		{"duplicate allowed value", "tags.json", `["Production", "Staging"]`, `["Production", "production"]`, LevelError, "duplicate value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := validFS()
			src := string(fsys[tc.file].Data)
			mutated := tc.to // an empty from replaces the whole file
			if tc.from != "" {
				mutated = strings.Replace(src, tc.from, tc.to, 1)
			}
			if mutated == src {
				t.Fatalf("fixture replacement %q did not apply", tc.from)
			}
			fsys[tc.file] = &fstest.MapFile{Data: []byte(mutated)}
			set, rep := LoadFS(fsys, defaultCatalog(t))

			if tc.want == "" {
				if len(rep.Diagnostics) > 0 {
					t.Fatalf("expected no diagnostics, got %v", rep.Diagnostics)
				}
				return
			}
			var hit bool
			for _, d := range rep.Diagnostics {
				if d.Level == tc.level && strings.Contains(d.String(), tc.want) {
					hit = true
				}
			}
			if !hit {
				t.Fatalf("no %s containing %q; got %v", tc.level, tc.want, rep.Diagnostics)
			}
			if (tc.level == LevelError) != (set == nil) {
				t.Errorf("set returned = %v for level %s", set != nil, tc.level)
			}
		})
	}
}

func TestSelect(t *testing.T) {
	set, _ := Load("../../policy", defaultCatalog(t))
	tests := []struct {
		sel  []string
		want int
	}{
		{nil, 17},
		{[]string{"database"}, 1},                // name, any case
		{[]string{"database_only_snapshots"}, 1}, // file stem
		{[]string{"Database*"}, 2},               // glob on name
		{[]string{"compute*", "storage"}, 4},
		{[]string{"Database", "database"}, 1}, // same group once
	}
	for _, tc := range tests {
		got, err := set.Select(tc.sel)
		if err != nil {
			t.Errorf("Select(%v): %v", tc.sel, err)
			continue
		}
		if len(got) != tc.want {
			t.Errorf("Select(%v) = %d groups, want %d", tc.sel, len(got), tc.want)
		}
	}
	if _, err := set.Select([]string{"nope"}); err == nil {
		t.Error("an unmatched selector must be an error")
	}
}

func TestMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	set, rep := Load(dir, defaultCatalog(t))
	if set != nil || rep.Err(false) == nil {
		t.Fatal("an empty directory must fail")
	}
	if err := os.WriteFile(dir+"/tags.json", []byte(fixtureTags), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, rep := Load(dir, defaultCatalog(t)); !strings.Contains(rep.Diagnostics[0].Message, "no group files") {
		t.Errorf("got %v", rep.Diagnostics)
	}
}
