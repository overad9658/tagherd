package engine

import (
	"testing"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
)

var (
	envDef    = &policy.TagDef{Name: "Environment", Allowed: []string{"Production", "Staging"}}
	deployDef = &policy.TagDef{Name: "DeploymentType", Allowed: []string{"Manual", "Automation"}}
	brandDef  = &policy.TagDef{Name: "Brand"}
	emailDef  = &policy.TagDef{Name: "AdminEmail"}
)

func tag(k, v string) model.Tag   { return model.Tag{Key: k, Value: v, Kind: model.KindTag} }
func label(k, v string) model.Tag { return model.Tag{Key: k, Value: v, Kind: model.KindLabel} }
func rmtag(k, v string) model.Tag { return model.Tag{Key: k, Value: v, Kind: model.KindRMTag} }

func TestCheckTag(t *testing.T) {
	tests := []struct {
		name  string
		cloud model.Cloud
		def   *policy.TagDef
		tags  []model.Tag
		want  result
	}{
		{"compliant", model.AWS, envDef, []model.Tag{tag("Environment", "Production")}, result{}},
		{"missing", model.AWS, envDef, nil, result{code: model.MissingTag}},
		{"missing deployment type proposes Manual", model.AWS, deployDef, nil,
			result{code: model.MissingTag, proposed: "Manual", fixable: true}},
		{"empty", model.AWS, brandDef, []model.Tag{tag("Brand", " ")}, result{code: model.EmptyValue, current: " "}},
		{"empty deployment type proposes Manual", model.AWS, deployDef, []model.Tag{tag("DeploymentType", "")},
			result{code: model.EmptyValue, proposed: "Manual", fixable: true}},
		{"invalid deployment type is never fixed", model.AWS, deployDef, []model.Tag{tag("DeploymentType", "Terraform")},
			result{code: model.InvalidDeploymentType, current: "Terraform"}},
		{"deployment type case", model.AWS, deployDef, []model.Tag{tag("DeploymentType", "manual")},
			result{code: model.CaseMismatch, current: "manual", proposed: "Manual", fixable: true}},
		{"invalid value", model.AWS, envDef, []model.Tag{tag("Environment", "prod")}, result{code: model.InvalidValue, current: "prod"}},
		{"value case", model.AWS, envDef, []model.Tag{tag("Environment", "production")},
			result{code: model.CaseMismatch, current: "production", proposed: "Production", fixable: true}},
		{"key case", model.AWS, envDef, []model.Tag{tag("environment", "Production")},
			result{code: model.CaseMismatch, current: "environment=Production", proposed: "Environment=Production", fixable: true}},
		{"key case on a free-form tag", model.AWS, brandDef, []model.Tag{tag("BRAND", "Acme")},
			result{code: model.CaseMismatch, current: "BRAND=Acme", proposed: "Brand=Acme", fixable: true}},
		{"free-form keeps its spelling", model.AWS, brandDef, []model.Tag{tag("Brand", "acme corp")}, result{}},
		{"duplicate key", model.AWS, envDef, []model.Tag{tag("Environment", "Production"), tag("environment", "Staging")},
			result{code: model.DuplicateKey, current: "Environment=Production, environment=Staging"}},
		{"same value twice is not a duplicate", model.AWS, envDef, []model.Tag{tag("environment", "production"), tag("Environment", "Production")}, result{}},
		{"valid email", model.AWS, emailDef, []model.Tag{tag("AdminEmail", "ops@example.com")}, result{}},
		{"email with a display name", model.AWS, emailDef, []model.Tag{tag("AdminEmail", "Ops <ops@example.com>")},
			result{code: model.InvalidValue, current: "Ops <ops@example.com>"}},
		{"not an email", model.AWS, emailDef, []model.Tag{tag("AdminEmail", "ops")}, result{code: model.InvalidValue, current: "ops"}},

		{"azure ignores key case", model.Azure, envDef, []model.Tag{tag("ENVIRONMENT", "Production")}, result{}},
		{"azure checks value case", model.Azure, envDef, []model.Tag{tag("Environment", "staging")},
			result{code: model.CaseMismatch, current: "staging", proposed: "Staging", fixable: true}},

		{"gcp lowercase label", model.GCP, envDef, []model.Tag{label("environment", "production")}, result{}},
		{"gcp label invalid", model.GCP, envDef, []model.Tag{label("environment", "prod")}, result{code: model.InvalidValue, current: "prod"}},
		{"gcp rm tag checks case", model.GCP, envDef, []model.Tag{rmtag("Environment", "production")},
			result{code: model.CaseMismatch, current: "production", proposed: "Production", fixable: true}},
		{"gcp label and rm tag agree", model.GCP, envDef, []model.Tag{label("environment", "production"), rmtag("Environment", "Production")}, result{}},
		{"gcp label and rm tag conflict", model.GCP, envDef, []model.Tag{label("environment", "staging"), rmtag("Environment", "Production")},
			result{code: model.DuplicateKey, current: "environment=staging, Environment=Production"}},
		{"gcp email as label is invalid", model.GCP, emailDef, []model.Tag{label("adminemail", "ops_example_com")},
			result{code: model.InvalidValue, current: "ops_example_com"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkTag(tc.cloud, tc.def, tc.tags); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
