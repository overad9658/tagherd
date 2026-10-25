package aws

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfgtypes "github.com/aws/aws-sdk-go-v2/service/configservice/types"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	fsxtypes "github.com/aws/aws-sdk-go-v2/service/fsx/types"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"golang.org/x/time/rate"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

// stub is a scripted backend.
type stub struct {
	mu        sync.Mutex
	accounts  []account
	mgmt      string
	prepare   map[string]error
	regions   []string
	rows      map[string][]string // by region ("agg" for the aggregator)
	selectErr map[string]error    // by region
	lists     map[string][]model.Resource
	listErr   map[string]error // by adapter/region
	calls     []string
}

func (s *stub) record(c string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
}

func (s *stub) Caller(context.Context) (identity, error) {
	return identity{Account: "111122223333", Partition: "aws"}, nil
}
func (s *stub) Accounts(context.Context) ([]account, error) { return s.accounts, nil }
func (s *stub) ManagementAccount(context.Context) (string, error) {
	if s.mgmt == "" {
		return "", errors.New("AWSOrganizationsNotInUseException")
	}
	return s.mgmt, nil
}
func (s *stub) Prepare(_ context.Context, a string) error { return s.prepare[a] }
func (s *stub) Regions(context.Context, string) ([]string, error) {
	return s.regions, nil
}
func (s *stub) SelectAggregate(_ context.Context, _ config.AWSAggregator, expr string) ([]string, error) {
	s.record("agg " + expr)
	return s.rows["agg"], nil
}
func (s *stub) Select(_ context.Context, _, region, expr string) ([]string, error) {
	s.record("select " + region)
	if err := s.selectErr[region]; err != nil {
		return nil, err
	}
	return s.rows[region], nil
}
func (s *stub) List(_ context.Context, adapter string, req listRequest) ([]model.Resource, error) {
	key := adapter + "/" + req.Region
	s.record("list " + key)
	if err := s.listErr[key]; err != nil {
		return nil, err
	}
	return s.lists[key], nil
}
func (s *stub) AccountTags(context.Context, string) ([]model.Tag, error) {
	return []model.Tag{{Key: "Brand", Value: "Acme", Kind: model.KindTag}}, nil
}

func entries(t *testing.T, displays ...string) []*catalog.Entry {
	t.Helper()
	cat, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	var out []*catalog.Entry
	for _, d := range displays {
		e, ok := cat.Lookup(model.AWS, d)
		if !ok {
			t.Fatalf("no catalog entry %q", d)
		}
		out = append(out, cat.Canonical(e))
	}
	return out
}

const rdsRow = `{"resourceId":"db-1","resourceName":"orders","resourceType":"AWS::RDS::DBInstance","awsRegion":"us-east-1","accountId":"111122223333","arn":"arn:aws:rds:us-east-1:111122223333:db:orders","resourceCreationTime":"2026-01-02T03:04:05.000Z","tags":[{"key":"Environment","value":"production","tag":"Environment=production"}],"configuration":{"engine":"postgres","dBClusterIdentifier":null}}`

const iamRow = `{"resourceId":"AROA1","resourceName":"deploy","resourceType":"AWS::IAM::Role","awsRegion":"global","accountId":"111122223333","arn":"arn:aws:iam::111122223333:role/deploy","tags":[]}`

func TestParseRows(t *testing.T) {
	rows := []string{
		rdsRow,
		`{"resourceId":"i-1","resourceType":"AWS::EC2::Instance","awsRegion":"us-east-1","accountId":"1","arn":"arn:aws:ec2:us-east-1:1:instance/i-1","tags":[{"key":"Name","value":"web-1"}]}`,
		`{"resourceId":"p","resourceName":"ReadOnlyAccess","resourceType":"AWS::IAM::Policy","awsRegion":"global","accountId":"1","arn":"arn:aws:iam::aws:policy/ReadOnlyAccess"}`,
		`{"resourceId":"c","resourceName":"c","resourceType":"AWS::RDS::DBInstance","awsRegion":"us-east-1","accountId":"1","arn":"x","configuration":{"engine":"mysql","dBClusterIdentifier":"aurora-1"}}`,
	}
	res, err := parseRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	db := res[0]
	if db.ID != "arn:aws:rds:us-east-1:111122223333:db:orders" || db.Name != "orders" || db.Region != "us-east-1" || db.Scope != "111122223333" {
		t.Errorf("rds = %+v", db)
	}
	if db.Created == nil || !db.Created.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("created = %v", db.Created)
	}
	if db.Attrs["engine"] != "postgres" || db.Attrs["cluster_member"] != "false" {
		t.Errorf("attrs = %v", db.Attrs)
	}
	if len(db.Tags) != 1 || db.Tags[0] != (model.Tag{Key: "Environment", Value: "production", Kind: model.KindTag}) {
		t.Errorf("tags = %+v", db.Tags)
	}
	if res[1].Name != "web-1" {
		t.Errorf("EC2 name falls back to the Name tag: %q", res[1].Name)
	}
	if res[2].Attrs["policy_owner"] != "aws" {
		t.Errorf("policy_owner = %q", res[2].Attrs["policy_owner"])
	}
	if res[3].Attrs["cluster_member"] != "true" {
		t.Errorf("cluster_member = %q", res[3].Attrs["cluster_member"])
	}
	if _, err := parseRows([]string{"{"}); err == nil {
		t.Error("bad JSON should fail")
	}
}

func TestBuildQueries(t *testing.T) {
	var natives []string
	for i := range 200 {
		natives = append(natives, "AWS::Service"+strings.Repeat("x", i%10)+"::Type"+string(rune('A'+i%26))+strings.Repeat("y", i/26))
	}
	natives = append(natives, "AWS::RDS::DBInstance")
	qs := buildQueries(natives, "111122223333")
	if len(qs) < 3 {
		t.Fatalf("expected batching, got %d queries", len(qs))
	}
	seen := 0
	for _, q := range qs {
		if len(q) > maxExpression {
			t.Errorf("query is %d characters", len(q))
		}
		if !strings.Contains(q, "accountId = '111122223333' AND ") {
			t.Errorf("missing account filter: %s", q)
		}
		seen += strings.Count(q, "'AWS::")
		if strings.Contains(q, "DBInstance") && !strings.Contains(q, "configuration.dBClusterIdentifier") {
			t.Errorf("RDS query lacks its properties: %s", q)
		}
	}
	if seen != len(natives) {
		t.Errorf("%d types in queries, want %d", seen, len(natives))
	}
	if q := buildQueries([]string{"AWS::S3::Bucket"}, ""); len(q) != 1 || strings.Contains(q[0], "accountId =") {
		t.Errorf("per-account query = %v", q)
	}
}

func TestScopes(t *testing.T) {
	b := &stub{accounts: []account{{ID: "111", Name: "prod"}, {ID: "222", Name: "dev"}, {ID: "333", Name: "sandbox"}}}
	p := newProvider(Options{Scopes: config.AWSScopes{Organization: true, ExcludeAccounts: []string{"333"}}}, b)
	got, err := p.Scopes(context.Background(), nil)
	if err != nil || len(got) != 2 || got[0].ID != "111" || got[1].Name != "dev" {
		t.Fatalf("scopes = %+v, %v", got, err)
	}
	if got, _ := p.Scopes(context.Background(), []string{"PRO*"}); len(got) != 1 || got[0].ID != "111" {
		t.Errorf("--account PRO* = %+v", got)
	}

	p = newProvider(Options{Scopes: config.AWSScopes{Accounts: []string{"444", "555"}}}, b)
	if got, _ := p.Scopes(context.Background(), nil); len(got) != 2 || got[0].ID != "444" {
		t.Errorf("literal accounts = %+v", got)
	}
	p = newProvider(Options{Scopes: config.AWSScopes{Accounts: []string{"4*"}}}, b)
	if _, err := p.Scopes(context.Background(), nil); err == nil {
		t.Error("a glob without organization should fail")
	}
	p = newProvider(Options{}, b)
	if got, _ := p.Scopes(context.Background(), nil); len(got) != 1 || got[0].ID != "111122223333" {
		t.Errorf("caller account = %+v", got)
	}
}

func TestDiscoverAggregator(t *testing.T) {
	b := &stub{rows: map[string][]string{"agg": {rdsRow, iamRow}}, regions: []string{"us-east-1"}}
	p := newProvider(Options{Scopes: config.AWSScopes{ConfigAggregator: &config.AWSAggregator{Name: "org"}}}, b)
	types := entries(t, "AWS RDS PostgreSQL Instance", "AWS IAM Role", "AWS Glue Crawler")
	d, err := p.Discover(context.Background(), provider.Scope{Cloud: model.AWS, ID: "111122223333"}, types)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Resources) != 2 || len(d.Errors) != 0 {
		t.Errorf("resources %d, errors %v", len(d.Resources), d.Errors)
	}
	if len(d.Uncovered) != 1 || d.Uncovered[0].Display != "AWS Glue Crawler" {
		t.Errorf("uncovered = %v", d.Uncovered)
	}
	for _, c := range b.calls {
		if strings.HasPrefix(c, "select") {
			t.Errorf("per-region Config query with an aggregator: %s", c)
		}
		if strings.HasPrefix(c, "agg") && !strings.Contains(c, "accountId = '111122223333'") {
			t.Errorf("aggregator query not restricted to the account: %s", c)
		}
	}
}

func TestDiscoverPerRegion(t *testing.T) {
	b := &stub{
		regions:   []string{"eu-west-1", "us-east-1", "us-west-2"},
		rows:      map[string][]string{"us-east-1": {rdsRow, iamRow}, "us-west-2": {iamRow}},
		selectErr: map[string]error{"eu-west-1": errors.New("AccessDenied")},
		mgmt:      "111122223333",
		lists: map[string][]model.Resource{
			"ec2/us-east-1":    {{Cloud: model.AWS, ID: "arn:ami-1", NativeType: "AWS::EC2::Image"}},
			"organizations/":   {{Cloud: model.AWS, ID: "arn:account", NativeType: "AWS::Organizations::Account"}},
			"ec2/us-west-2":    nil,
			"ec2/eu-west-1":    nil,
			"organizations/x/": nil,
		},
	}
	p := newProvider(Options{}, b)
	types := entries(t, "AWS RDS PostgreSQL Instance", "AWS IAM Role", "AWS Machine Image (AMI)", "AWS Account")
	d, err := p.Discover(context.Background(), provider.Scope{Cloud: model.AWS, ID: "111122223333"}, types)
	if err != nil {
		t.Fatal(err)
	}
	// The IAM role is recorded in two regions but reported once.
	ids := make([]string, len(d.Resources))
	for i, r := range d.Resources {
		ids[i] = r.ID
	}
	want := []string{"arn:account", "arn:ami-1", "arn:aws:iam::111122223333:role/deploy", "arn:aws:rds:us-east-1:111122223333:db:orders"}
	if !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if len(d.Errors) != 1 || !strings.Contains(d.Errors[0].Error(), "AWS Config in eu-west-1: AccessDenied") {
		t.Errorf("errors = %v", d.Errors)
	}
	orgCalls := 0
	for _, c := range b.calls {
		if strings.HasPrefix(c, "list organizations") {
			orgCalls++
		}
	}
	if orgCalls != 1 {
		t.Errorf("organizations is global: %d calls", orgCalls)
	}
}

func TestDiscoverScopeFailures(t *testing.T) {
	b := &stub{prepare: map[string]error{"222": errors.New("AccessDenied: sts:AssumeRole")}}
	p := newProvider(Options{Scopes: config.AWSScopes{Regions: []string{"us-east-1"}}}, b)
	if _, err := p.Discover(context.Background(), provider.Scope{Cloud: model.AWS, ID: "222"}, entries(t, "AWS IAM Role")); err == nil {
		t.Error("a role that cannot be assumed fails the scope")
	}
	b.listErr = map[string]error{"ec2/us-east-1": errors.New("UnauthorizedOperation")}
	d, err := p.Discover(context.Background(), provider.Scope{Cloud: model.AWS, ID: "111"}, entries(t, "AWS EBS Encrypted Snapshot"))
	if err != nil || len(d.Errors) != 1 || !strings.Contains(d.Errors[0].Error(), "AWS EBS Encrypted Snapshot in us-east-1") {
		t.Errorf("err %v, errors %v", err, d.Errors)
	}
}

func TestParentTagsCached(t *testing.T) {
	p := newProvider(Options{}, &stub{})
	for range 2 {
		tags, err := p.ParentTags(context.Background(), model.Resource{Scope: "111"})
		if err != nil || len(tags) != 1 {
			t.Fatalf("tags = %v, %v", tags, err)
		}
	}
	if _, err := p.Writer(context.Background(), provider.Scope{}); !errors.Is(err, provider.ErrWritesNotYet) {
		t.Errorf("writer err = %v", err)
	}
}

// TestCatalogCoverage ties the catalog to this adapter: every bulk type is
// one AWS Config records, and every list type has a lister that lists it.
func TestCatalogCoverage(t *testing.T) {
	cat, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]bool{}
	for _, v := range cfgtypes.ResourceType("").Values() {
		recorded[string(v)] = true
	}
	for _, e := range cat.Entries {
		if e.Cloud != model.AWS || e.AliasOf != "" {
			continue
		}
		switch mode, adapter := e.DiscoveryMode(); mode {
		case catalog.DiscoverBulk:
			n := 0
			for _, nt := range e.NativeTypes {
				if recorded[nt] {
					n++
				}
			}
			if n == 0 {
				t.Errorf("%q: AWS Config records none of %v; use a list adapter or discovery: none", e.Display, e.NativeTypes)
			}
		case catalog.DiscoverList:
			l, ok := listers[adapter]
			if !ok {
				t.Errorf("%q: no lister %q", e.Display, adapter)
				continue
			}
			for _, nt := range e.NativeTypes {
				if !slices.Contains(l.natives, nt) {
					t.Errorf("%q: lister %q does not list %s", e.Display, adapter, nt)
				}
			}
		}
	}
}

func TestConverters(t *testing.T) {
	req := listRequest{Account: "111", Region: "us-east-1", Partition: "aws"}
	img := imageResource(req, ec2types.Image{ImageId: aws.String("ami-1"), Name: aws.String("base"), CreationDate: aws.String("2026-03-04T05:06:07.000Z")})
	if img.ID != "arn:aws:ec2:us-east-1:111:image/ami-1" || img.Name != "base" || img.Created == nil {
		t.Errorf("image = %+v", img)
	}
	snap := snapshotResource(req, ec2types.Snapshot{SnapshotId: aws.String("snap-1"), Encrypted: aws.Bool(true),
		Tags: []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("nightly")}, {Key: aws.String("Brand"), Value: aws.String("Acme")}}})
	if snap.Attrs["encrypted"] != "true" || snap.Name != "nightly" || snap.Tags[0].Key != "Brand" {
		t.Errorf("snapshot = %+v", snap)
	}
	fs := fileSystemResource(req, fsxtypes.FileSystem{FileSystemId: aws.String("fs-1"), ResourceARN: aws.String("arn:fs-1"), FileSystemType: fsxtypes.FileSystemTypeOntap})
	if fs.Attrs["file_system_type"] != "ONTAP" || fs.Name != "fs-1" {
		t.Errorf("fsx = %+v", fs)
	}
	task := taskResource(req, ecstypes.Task{TaskArn: aws.String("arn:aws:ecs:us-east-1:111:task/web/abc123")})
	if task.Name != "abc123" || task.Tags == nil {
		t.Errorf("task = %+v", task)
	}
}

// orgStub is a fake Organizations client.
type orgStub struct{}

func (orgStub) ListTagsForResource(_ context.Context, in *organizations.ListTagsForResourceInput, _ ...func(*organizations.Options)) (*organizations.ListTagsForResourceOutput, error) {
	return &organizations.ListTagsForResourceOutput{Tags: []orgtypes.Tag{{Key: aws.String("Owner"), Value: aws.String(aws.ToString(in.ResourceId))}}}, nil
}

func (orgStub) ListPolicies(_ context.Context, in *organizations.ListPoliciesInput, _ ...func(*organizations.Options)) (*organizations.ListPoliciesOutput, error) {
	return &organizations.ListPoliciesOutput{Policies: []orgtypes.PolicySummary{
		{Id: aws.String("p-full"), Name: aws.String("FullAWSAccess"), AwsManaged: true},
		{Id: aws.String("p-" + string(in.Filter)), Arn: aws.String("arn:p"), Name: aws.String("deny-regions")},
	}}, nil
}

func TestListOrganizations(t *testing.T) {
	limit := rate.NewLimiter(rate.Inf, 1)
	natives := []string{"AWS::Organizations::Account", "AWS::Organizations::Policy"}
	req := listRequest{Account: "111", Partition: "aws", NativeTypes: natives, AccountName: "prod"}
	res, err := listOrganizations(context.Background(), orgStub{}, limit, req)
	if err != nil || len(res) != 1 || res[0].Name != "prod" || res[0].Tags[0].Value != "111" {
		t.Fatalf("member account: %+v, %v", res, err)
	}
	req.Management = true
	res, err = listOrganizations(context.Background(), orgStub{}, limit, req)
	if err != nil || len(res) != 3 {
		t.Fatalf("management account: %+v, %v", res, err)
	}
	if res[1].Attrs["policy_type"] != "SERVICE_CONTROL_POLICY" || res[2].Attrs["policy_type"] != "RESOURCE_CONTROL_POLICY" {
		t.Errorf("policies = %+v", res[1:])
	}
}
