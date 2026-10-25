package aws

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/configservice"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"golang.org/x/time/rate"

	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
)

// defaultRegion is used for global APIs when the profile names no region.
const defaultRegion = "us-east-1"

// sdkBackend implements backend with the AWS SDK.
type sdkBackend struct {
	base      aws.Config
	readRole  string
	writeRole string

	mu       sync.Mutex
	accounts map[string]aws.Config // read-role credentials by account
	writers  map[string]aws.Config // write-role credentials by account
	caller   *identity

	// APIs that every account's reads share are rate-limited client-side;
	// the SDK's adaptive retryer handles throttling on the rest.
	orgLimit *rate.Limiter
	aggLimit *rate.Limiter
}

func newSDKBackend(ctx context.Context, opts Options) (*sdkBackend, error) {
	profile := opts.Profile
	if profile == "" {
		profile = opts.Scopes.Profile
	}
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRetryer(func() aws.Retryer {
			return retry.NewAdaptiveMode(func(o *retry.AdaptiveModeOptions) {
				o.StandardOptions = append(o.StandardOptions, func(so *retry.StandardOptions) { so.MaxAttempts = 8 })
			})
		}),
	}
	if profile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(profile))
	}
	base, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("AWS credentials: %w", err)
	}
	if base.Region == "" {
		base.Region = defaultRegion
	}
	return &sdkBackend{
		base:      base,
		readRole:  opts.Scopes.ReadRoleName,
		writeRole: opts.Scopes.WriteRoleName,
		accounts:  map[string]aws.Config{},
		writers:   map[string]aws.Config{},
		orgLimit:  rate.NewLimiter(2, 2),
		aggLimit:  rate.NewLimiter(5, 5),
	}, nil
}

func (b *sdkBackend) Caller(ctx context.Context) (identity, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.caller != nil {
		return *b.caller, nil
	}
	out, err := sts.NewFromConfig(b.base).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return identity{}, err
	}
	id := identity{Account: aws.ToString(out.Account), Partition: "aws"}
	if a, err := arn.Parse(aws.ToString(out.Arn)); err == nil {
		id.Partition = a.Partition
	}
	b.caller = &id
	return id, nil
}

// account returns read-role credentials for an account, in region.
func (b *sdkBackend) account(ctx context.Context, account, region string) (aws.Config, error) {
	return b.assumed(ctx, b.accounts, b.readRole, "tagherd-read", account, region)
}

// writer returns write-role credentials for an account, in region.
func (b *sdkBackend) writer(ctx context.Context, account, region string) (aws.Config, error) {
	return b.assumed(ctx, b.writers, b.writeRole, "tagherd-write", account, region)
}

// assumed returns cached credentials for role in account; an empty role
// means the base credentials.
func (b *sdkBackend) assumed(ctx context.Context, cache map[string]aws.Config, role, session, account, region string) (aws.Config, error) {
	b.mu.Lock()
	cfg, ok := cache[account]
	b.mu.Unlock()
	if !ok {
		cfg = b.base.Copy()
		if role != "" {
			id, err := b.Caller(ctx)
			if err != nil {
				return aws.Config{}, err
			}
			roleARN := fmt.Sprintf("arn:%s:iam::%s:role/%s", id.Partition, account, role)
			cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(b.base), roleARN,
				func(o *stscreds.AssumeRoleOptions) { o.RoleSessionName = session }))
		}
		b.mu.Lock()
		cache[account] = cfg
		b.mu.Unlock()
	}
	c := cfg.Copy()
	if region != "" {
		c.Region = region
	}
	return c, nil
}

func (b *sdkBackend) Prepare(ctx context.Context, account string) error {
	cfg, err := b.account(ctx, account, "")
	if err != nil {
		return err
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		if b.readRole != "" {
			return fmt.Errorf("assuming %s: %w", b.readRole, err)
		}
		return err
	}
	return nil
}

func (b *sdkBackend) Accounts(ctx context.Context) ([]account, error) {
	var out []account
	pg := organizations.NewListAccountsPaginator(organizations.NewFromConfig(b.base), &organizations.ListAccountsInput{})
	for pg.HasMorePages() {
		if err := b.orgLimit.Wait(ctx); err != nil {
			return nil, err
		}
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range page.Accounts {
			if a.State != orgtypes.AccountStateActive {
				continue
			}
			out = append(out, account{ID: aws.ToString(a.Id), Name: aws.ToString(a.Name), ARN: aws.ToString(a.Arn)})
		}
	}
	return out, nil
}

func (b *sdkBackend) ManagementAccount(ctx context.Context) (string, error) {
	if err := b.orgLimit.Wait(ctx); err != nil {
		return "", err
	}
	out, err := organizations.NewFromConfig(b.base).DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.Organization.MasterAccountId), nil
}

func (b *sdkBackend) AccountTags(ctx context.Context, account string) ([]model.Tag, error) {
	return orgTags(ctx, organizations.NewFromConfig(b.base), b.orgLimit, account)
}

func (b *sdkBackend) Regions(ctx context.Context, account string) ([]string, error) {
	cfg, err := b.account(ctx, account, "")
	if err != nil {
		return nil, err
	}
	out, err := ec2.NewFromConfig(cfg).DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		return nil, err
	}
	regions := make([]string, 0, len(out.Regions))
	for _, r := range out.Regions {
		regions = append(regions, aws.ToString(r.RegionName))
	}
	return regions, nil
}

func (b *sdkBackend) SelectAggregate(ctx context.Context, agg config.AWSAggregator, expr string) ([]string, error) {
	cfg := b.base.Copy()
	if agg.Region != "" {
		cfg.Region = agg.Region
	}
	pg := configservice.NewSelectAggregateResourceConfigPaginator(configservice.NewFromConfig(cfg), &configservice.SelectAggregateResourceConfigInput{
		ConfigurationAggregatorName: aws.String(agg.Name), Expression: aws.String(expr), Limit: 100,
	})
	var rows []string
	for pg.HasMorePages() {
		if err := b.aggLimit.Wait(ctx); err != nil {
			return nil, err
		}
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		rows = append(rows, page.Results...)
	}
	return rows, nil
}

func (b *sdkBackend) Select(ctx context.Context, account, region, expr string) ([]string, error) {
	cfg, err := b.account(ctx, account, region)
	if err != nil {
		return nil, err
	}
	pg := configservice.NewSelectResourceConfigPaginator(configservice.NewFromConfig(cfg), &configservice.SelectResourceConfigInput{
		Expression: aws.String(expr), Limit: 100,
	})
	var rows []string
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		rows = append(rows, page.Results...)
	}
	return rows, nil
}

func (b *sdkBackend) List(ctx context.Context, adapter string, req listRequest) ([]model.Resource, error) {
	l, ok := listers[adapter]
	if !ok {
		return nil, fmt.Errorf("no list adapter %q", adapter)
	}
	if adapter == "organizations" {
		// Organizations is read with the base credentials, which must belong
		// to the management account or a delegated administrator.
		return listOrganizations(ctx, organizations.NewFromConfig(b.base), b.orgLimit, req)
	}
	cfg, err := b.account(ctx, req.Account, req.Region)
	if err != nil {
		return nil, err
	}
	res, err := l.run(ctx, cfg, req)
	if notInRegion(err) {
		return nil, nil // the service has no endpoint in this region
	}
	return res, err
}
