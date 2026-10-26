package terraform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
)

var fixtureDir = filepath.Join("..", "..", "testdata", "fixtures", "terraform")

var (
	awsRepo = &model.RepoRef{SCM: "GitHub", Org: "acme", Repo: "infra-aws"}
	gcpRepo = &model.RepoRef{SCM: "bitbucket", Org: "acme-ws", Repo: "infra-gcp"}
	allow   = []config.Repository{{SCM: "GitHub", Org: "acme", Repo: "infra-*"}, {SCM: "BitBucket", Org: "acme-ws", Repo: "*"}}
)

func awsChange(id, tag, old, value string) model.Change {
	action := model.ActionAdd
	if old != "" {
		action = model.ActionUpdate
	}
	return model.Change{ResourceID: id, Cloud: model.AWS, Scope: "111122223333", Tag: tag, Action: action,
		Old: old, New: value, Expect: old, Rule: "set", Route: model.RouteTerraform, Repo: awsRepo}
}

func newFixtureRouter(t *testing.T, pr bool) (*Router, *Fixture, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	work := t.TempDir()
	f := &Fixture{Dir: fixtureDir, Work: work}
	patches := filepath.Join(work, "patches")
	rt := New(Options{
		Allowed: allow, PullRequests: pr, PatchDir: patches, Concurrency: 2, WorkDir: work,
		RemoteURL: f.RemoteURL, States: f, Pulls: f,
	})
	return rt, f, patches
}

func outcomes(rs []model.ChangeResult) []model.Outcome {
	out := make([]model.Outcome, len(rs))
	for i, r := range rs {
		out[i] = r.Outcome
	}
	return out
}

func TestRouterFixture(t *testing.T) {
	rt, f, patches := newFixtureRouter(t, true)
	cs := []model.Change{
		awsChange("arn:aws:s3:::acme-data", "Environment", "prod", "Production"),
		awsChange("arn:aws:s3:::acme-data", "OwningOrg", "", "Platform"),
		awsChange("arn:aws:lambda:us-east-1:111122223333:function:report-gen", "OwningOrg", "", "Platform"),
		awsChange("arn:aws:dynamodb:us-east-1:111122223333:table/ledger", "OwningOrg", "", "Platform"), // already set
		awsChange("arn:aws:ec2:us-east-1:111122223333:instance/i-0etl", "OwningOrg", "", "Platform"),
		awsChange("arn:aws:s3:::acme-tmp", "OwningOrg", "", "Platform"),
		{ResourceID: "//compute.googleapis.com/projects/acme-prod/zones/us-central1-a/instances/batch-1", Cloud: model.GCP,
			Tag: "environment", Kind: model.KindLabel, Action: model.ActionUpdate, Old: "prod", New: "Production", Repo: gcpRepo},
	}
	cs[5].Repo = awsRepo
	got := rt.Run(context.Background(), "01PLAN", cs)

	want := []model.Outcome{model.OutcomeProposed, model.OutcomeProposed, model.OutcomeProposed, model.OutcomeNoop,
		model.OutcomeUnroutable, model.OutcomeUnroutable, model.OutcomeProposed}
	if !reflect.DeepEqual(outcomes(got), want) {
		for _, r := range got {
			t.Logf("%s %s: %s %s", r.ResourceID, r.Tag, r.Outcome, r.Detail)
		}
		t.Fatalf("outcomes = %v, want %v", outcomes(got), want)
	}
	lambda := got[2].Terraform
	if lambda.Root != "live/prod" || lambda.Address != "module.reports.aws_lambda_function.this" ||
		lambda.Block != "module.reports" || lambda.File != "live/prod/main.tf" {
		t.Errorf("lambda target = %+v", lambda)
	}
	if !strings.HasPrefix(got[4].Detail, model.UnroutableUnsupported) || !strings.HasPrefix(got[5].Detail, model.UnroutableNotInState) {
		t.Errorf("details: %q, %q", got[4].Detail, got[5].Detail)
	}

	patch, err := os.ReadFile(filepath.Join(patches, "github-acme-infra-aws.patch"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`+    Environment        = "Production"`, `+    OwningOrg          = "Platform"`, `+    OwningOrg   = "Platform"`} {
		if !strings.Contains(string(patch), s) {
			t.Errorf("patch lacks %q:\n%s", s, patch)
		}
	}
	if strings.Contains(string(patch), "show.json") || strings.Contains(string(patch), "ledger") {
		t.Errorf("patch touches more than the edits:\n%s", patch)
	}
	gcp, _ := os.ReadFile(filepath.Join(patches, "bitbucket-acme-ws-infra-gcp.patch"))
	if !strings.Contains(string(gcp), `+    environment    = "production"`) {
		t.Errorf("GCP labels are not lowercase:\n%s", gcp)
	}

	// One branch and one pull request per repository.
	if len(f.Opened) != 2 {
		t.Fatalf("opened %d pull requests", len(f.Opened))
	}
	for _, pr := range f.Opened {
		if pr.Head != "tagherd/01PLAN" || pr.Base != "main" || !strings.Contains(pr.Body, "01PLAN") {
			t.Errorf("pull request = %+v", pr)
		}
	}
	if !strings.HasPrefix(got[0].Terraform.PullURL, "https://github.example/acme/infra-aws/pull/") || got[0].Detail != got[0].Terraform.PullURL {
		t.Errorf("result = %+v", got[0])
	}
	remote := f.Remote(*awsRepo)
	show := exec.Command("git", "show", "tagherd/01PLAN:live/prod/main.tf")
	show.Dir = remote
	if out, err := show.Output(); err != nil || !strings.Contains(string(out), `OwningOrg          = "Platform"`) {
		t.Errorf("pushed branch: %v\n%s", err, out)
	}

	// Once the branch is merged, the same plan finds every tag set.
	merge := exec.Command("git", "merge", "--quiet", "--ff-only", "tagherd/01PLAN")
	merge.Dir = remote
	if out, err := merge.CombinedOutput(); err != nil {
		t.Fatalf("merge: %v %s", err, out)
	}
	_ = os.Remove(filepath.Join(patches, "github-acme-infra-aws.patch"))
	again := rt.Run(context.Background(), "01PLAN", cs[:4])
	for _, r := range again {
		if r.Outcome != model.OutcomeNoop {
			t.Errorf("rerun: %s %s is %s %s", r.ResourceID, r.Tag, r.Outcome, r.Detail)
		}
	}
	if _, err := os.Stat(filepath.Join(patches, "github-acme-infra-aws.patch")); !os.IsNotExist(err) {
		t.Errorf("rerun wrote a patch: %v", err)
	}
}

func TestRouterUnroutableBeforeClone(t *testing.T) {
	rt := New(Options{Allowed: allow, RemoteURL: func(model.RepoRef) (string, error) {
		t.Error("nothing should be cloned")
		return "", errors.New("no")
	}})
	noRepo := awsChange("arn:a", "OwningOrg", "", "Platform")
	noRepo.Repo = nil
	other := awsChange("arn:b", "OwningOrg", "", "Platform")
	other.Repo = &model.RepoRef{SCM: "GitHub", Org: "acme", Repo: "legacy"}
	gitlab := awsChange("arn:c", "OwningOrg", "", "Platform")
	gitlab.Repo = &model.RepoRef{SCM: "GitLab", Org: "acme", Repo: "infra-x"}
	got := rt.Run(context.Background(), "P", []model.Change{noRepo, other, gitlab})
	wantPrefix := []string{model.UnroutableMissingRepoTags, model.UnroutableNotAllowlisted, model.UnroutableNotAllowlisted}
	for i, r := range got {
		if r.Outcome != model.OutcomeUnroutable || !strings.HasPrefix(r.Detail, wantPrefix[i]) {
			t.Errorf("%d: %s %q", i, r.Outcome, r.Detail)
		}
	}
}

func TestRouterWithoutPR(t *testing.T) {
	rt, f, patches := newFixtureRouter(t, false)
	a := awsChange("arn:aws:lambda:us-east-1:111122223333:function:report-gen", "OwningOrg", "", "Platform")
	b := awsChange("arn:aws:lambda:us-east-1:111122223333:function:report-gen", "Brand", "Acme", "Acme Corp")
	got := rt.Run(context.Background(), "P", []model.Change{a, b})
	if got[0].Outcome != model.OutcomeProposed || got[0].Detail != filepath.Join(patches, "github-acme-infra-aws.patch") {
		t.Errorf("without --pr the detail is the patch: %+v", got[0])
	}
	// Brand comes from local.common_tags; the new value goes in the object.
	if got[1].Outcome != model.OutcomeProposed {
		t.Errorf("brand: %+v", got[1])
	}
	if len(f.Opened) != 0 {
		t.Errorf("pull requests opened without --pr")
	}
}

func TestEditBlockConflict(t *testing.T) {
	dir := t.TempDir()
	src := "module \"reports\" {\n  tags = {\n    Brand = \"Acme\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := New(Options{})
	out := []model.ChangeResult{
		{Change: awsChange("arn:1", "OwningOrg", "", "Platform"), Terraform: &model.TerraformTarget{}},
		{Change: awsChange("arn:2", "OwningOrg", "", "Finance"), Terraform: &model.TerraformTarget{}},
		{Change: awsChange("arn:2", "Exposure", "", "Internal"), Terraform: &model.TerraformTarget{}},
	}
	file := rt.editBlock(dir, ".", Block{Type: "module", Labels: []string{"reports"}, Attr: "tags"}, []int{0, 1, 2}, out)
	if out[0].Outcome != model.OutcomeUnroutable || out[1].Outcome != model.OutcomeUnroutable || out[2].Outcome != model.OutcomeProposed {
		t.Errorf("outcomes = %v", outcomes(out))
	}
	if file != "main.tf" {
		t.Errorf("file = %q", file)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "main.tf"))
	if strings.Contains(string(data), "OwningOrg") {
		t.Errorf("a conflicting key was written:\n%s", data)
	}
}

type brokenStates struct{}

func (brokenStates) Show(context.Context, string) ([]byte, error) {
	return nil, errors.New("backend: AccessDenied")
}

func TestRouterUnreadableStateFails(t *testing.T) {
	rt, _, _ := newFixtureRouter(t, false)
	rt.opts.States = brokenStates{}
	got := rt.Run(context.Background(), "P", []model.Change{awsChange("arn:aws:s3:::acme-data", "OwningOrg", "", "Platform")})
	if got[0].Outcome != model.OutcomeFailed || !strings.Contains(got[0].Detail, "AccessDenied") {
		t.Errorf("got %+v", got[0])
	}
}

func TestAllowed(t *testing.T) {
	tests := []struct {
		ref  model.RepoRef
		want string
	}{
		{model.RepoRef{SCM: "github", Org: "ACME", Repo: "Infra-Net"}, ""},
		{model.RepoRef{SCM: "Bitbucket", Org: "acme-ws", Repo: "anything"}, ""},
		{model.RepoRef{SCM: "GitHub", Org: "acme", Repo: "apps"}, model.UnroutableNotAllowlisted},
		{model.RepoRef{SCM: "GitHub", Org: "acme", Repo: ".."}, model.UnroutableNotAllowlisted},
		{model.RepoRef{SCM: "GitHub", Org: "acme", Repo: "infra-a/b"}, model.UnroutableNotAllowlisted},
	}
	for _, tc := range tests {
		got := allowed(tc.ref, allow)
		if (tc.want == "") != (got == "") || !strings.HasPrefix(got, tc.want) {
			t.Errorf("%v: %q", tc.ref, got)
		}
	}
}

func TestLocate(t *testing.T) {
	show, err := os.ReadFile(filepath.Join(fixtureDir, "github/acme/infra-aws/live/prod/show.json"))
	if err != nil {
		t.Fatal(err)
	}
	lambda := "arn:aws:lambda:us-east-1:111122223333:function:report-gen"
	got, err := locate(show, map[string]model.Change{
		lambda:      {ResourceID: lambda, Cloud: model.AWS},
		"acme-data": {ResourceID: "acme-data", Cloud: model.AWS}, // matched by id
		"arn:none":  {ResourceID: "arn:none", Cloud: model.AWS},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Address{
		lambda:      {Full: "module.reports.aws_lambda_function.this", Modules: []string{"reports"}, Type: "aws_lambda_function", Name: "this"},
		"acme-data": {Full: "aws_s3_bucket.data", Type: "aws_s3_bucket", Name: "data"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestModuleNames(t *testing.T) {
	tests := map[string][]string{
		"":                                    nil,
		"module.a":                            {"a"},
		`module.a["x.y"].module.b[0]`:         {"a", "b"},
		`module.a["]"].module.b`:              {"a", "b"},
		`module.app["k\"]"].module.inner[12]`: {"app", "inner"},
	}
	for in, want := range tests {
		got, err := moduleNames(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := moduleNames("aws_s3_bucket.x"); err == nil {
		t.Error("a resource address is not a module address")
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		c      model.Change
		values map[string]any
		want   bool
	}{
		{model.Change{Cloud: model.AWS, ResourceID: "arn:aws:s3:::b"}, map[string]any{"arn": "arn:aws:s3:::b"}, true},
		{model.Change{Cloud: model.AWS, ResourceID: "arn:aws:s3:::b"}, map[string]any{"arn": "arn:aws:s3:::c"}, false},
		{model.Change{Cloud: model.Azure, ResourceID: "/subscriptions/1/resourceGroups/RG/providers/x/y"},
			map[string]any{"id": "/subscriptions/1/resourcegroups/rg/providers/x/y"}, true},
		{model.Change{Cloud: model.GCP, ResourceID: "//compute.googleapis.com/projects/p/zones/z/instances/i"},
			map[string]any{"self_link": "https://www.googleapis.com/compute/v1/projects/p/zones/z/instances/i"}, true},
		{model.Change{Cloud: model.GCP, ResourceID: "//storage.googleapis.com/projects/_/buckets/b"},
			map[string]any{"id": "b"}, true},
		{model.Change{Cloud: model.GCP, ResourceID: "//compute.googleapis.com/projects/p/zones/z/instances/i"},
			map[string]any{"id": "projects/p/zones/z/instances/i2"}, false},
	}
	for i, tc := range tests {
		if got := matches(tc.c, tc.values); got != tc.want {
			t.Errorf("%d: got %v", i, got)
		}
	}
}

func TestFindRoots(t *testing.T) {
	roots, err := findRoots(filepath.Join(fixtureDir, "github/acme/infra-aws"))
	if err != nil || !reflect.DeepEqual(roots, []string{"live/dev", "live/prod"}) {
		t.Errorf("roots = %v, %v", roots, err)
	}
}

func TestTokenVar(t *testing.T) {
	if got := tokenVar(registryHost("https://acme.jfrog-io.example.com/artifactory")); got != "TF_TOKEN_acme_jfrog__io_example_com" {
		t.Errorf("got %s", got)
	}
}

func TestGitHubPullRequest(t *testing.T) {
	var created map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/infra-aws/pulls":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &created)
			if created["head"] == "tagherd/again" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = io.WriteString(w, `{"message":"A pull request already exists"}`)
				return
			}
			_, _ = io.WriteString(w, `{"html_url":"https://github.com/acme/infra-aws/pull/7"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/infra-aws/pulls" && r.URL.Query().Get("head") == "acme:tagherd/again":
			_, _ = io.WriteString(w, `[{"html_url":"https://github.com/acme/infra-aws/pull/3"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	api := API{Tokens: config.SCMTokens{GitHub: "gh-token"}, GitHubURL: srv.URL}

	url, err := api.Open(context.Background(), *awsRepo, PullRequest{Title: "t", Body: "b", Head: "tagherd/P", Base: "main"})
	if err != nil || url != "https://github.com/acme/infra-aws/pull/7" || created["base"] != "main" {
		t.Errorf("url %q, err %v, body %v", url, err, created)
	}
	url, err = api.Open(context.Background(), *awsRepo, PullRequest{Head: "tagherd/again", Base: "main"})
	if err != nil || url != "https://github.com/acme/infra-aws/pull/3" {
		t.Errorf("an open pull request is reused: %q %v", url, err)
	}
	if _, err := (API{GitHubURL: srv.URL}).Open(context.Background(), *awsRepo, PullRequest{}); err == nil || !strings.Contains(err.Error(), "scm_tokens.github") {
		t.Errorf("no token: %v", err)
	}
}

func TestBitbucketPullRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Source struct {
				Branch struct{ Name string } `json:"branch"`
			} `json:"source"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/repositories/acme-ws/infra-gcp/pullrequests" || body.Source.Branch.Name != "tagherd/P" {
			http.Error(w, `{"error":{"message":"bad"}}`, http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"links":{"html":{"href":"https://bitbucket.org/acme-ws/infra-gcp/pull-requests/4"}}}`)
	}))
	defer srv.Close()
	api := API{Tokens: config.SCMTokens{Bitbucket: "bb"}, BitbucketURL: srv.URL}
	url, err := api.Open(context.Background(), *gcpRepo, PullRequest{Head: "tagherd/P", Base: "main"})
	if err != nil || url != "https://bitbucket.org/acme-ws/infra-gcp/pull-requests/4" {
		t.Errorf("url %q, err %v", url, err)
	}
	if _, err := api.Open(context.Background(), *gcpRepo, PullRequest{Head: "x"}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %v", err)
	}
}
