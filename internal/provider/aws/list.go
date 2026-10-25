package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/fsx"
	fsxtypes "github.com/aws/aws-sdk-go-v2/service/fsx/types"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/sagemaker"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"golang.org/x/time/rate"

	"github.com/overad9658/tagherd/internal/model"
)

// lister is one per-service list adapter, named in the catalog as
// discovery: list:<name>. A global lister runs once per account; the rest
// run once per region.
type lister struct {
	global  bool
	natives []string // the native types it lists
	run     func(ctx context.Context, cfg aws.Config, req listRequest) ([]model.Resource, error)
}

var listers = map[string]lister{
	// Run by sdkBackend.List with the base credentials.
	"organizations": {global: true, natives: []string{"AWS::Organizations::Account", "AWS::Organizations::Policy"}},
	"ec2":           {run: listEC2, natives: []string{"AWS::EC2::Image", "AWS::EC2::KeyPair", "AWS::EC2::Snapshot"}},
	"ecs":           {run: listECS, natives: []string{"AWS::ECS::Task"}},
	"elasticache":   {run: listElastiCache, natives: []string{"AWS::ElastiCache::CacheCluster", "AWS::ElastiCache::ReplicationGroup"}},
	"fsx":           {run: listFSx, natives: []string{"AWS::FSx::FileCache", "AWS::FSx::FileSystem"}},
	"logs":          {run: listLogs, natives: []string{"AWS::Logs::LogGroup"}},
	"ssm":           {run: listSSM, natives: []string{"AWS::SSM::Parameter"}},
	"sagemaker":     {run: listSageMaker, natives: []string{"AWS::SageMaker::NotebookInstance"}},
	"lightsail":     {run: listLightsail, natives: []string{"AWS::Lightsail::Instance"}},
}

// notInRegion reports an endpoint that does not exist, which is how a
// service that is not offered in a region fails.
func notInRegion(err error) bool {
	var dns *net.DNSError
	return errors.As(err, &dns) && dns.IsNotFound
}

func regionalARN(req listRequest, service, resource string) string {
	return fmt.Sprintf("arn:%s:%s:%s:%s:%s", req.Partition, service, req.Region, req.Account, resource)
}

// ---------------------------------------------------------------- EC2

func listEC2(ctx context.Context, cfg aws.Config, req listRequest) ([]model.Resource, error) {
	c := ec2.NewFromConfig(cfg)
	var out []model.Resource
	if req.wants("AWS::EC2::Image") {
		pg := ec2.NewDescribeImagesPaginator(c, &ec2.DescribeImagesInput{Owners: []string{"self"}})
		for pg.HasMorePages() {
			page, err := pg.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("DescribeImages: %w", err)
			}
			for _, img := range page.Images {
				out = append(out, imageResource(req, img))
			}
		}
	}
	if req.wants("AWS::EC2::Snapshot") {
		pg := ec2.NewDescribeSnapshotsPaginator(c, &ec2.DescribeSnapshotsInput{OwnerIds: []string{"self"}})
		for pg.HasMorePages() {
			page, err := pg.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("DescribeSnapshots: %w", err)
			}
			for _, s := range page.Snapshots {
				out = append(out, snapshotResource(req, s))
			}
		}
	}
	if req.wants("AWS::EC2::KeyPair") {
		page, err := c.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{})
		if err != nil {
			return nil, fmt.Errorf("DescribeKeyPairs: %w", err)
		}
		for _, k := range page.KeyPairs {
			tags := ec2Tags(k.Tags)
			out = append(out, resource(req, "AWS::EC2::KeyPair", regionalARN(req, "ec2", "key-pair/"+aws.ToString(k.KeyPairId)),
				aws.ToString(k.KeyName), k.CreateTime, tags))
		}
	}
	return out, nil
}

func imageResource(req listRequest, img ec2types.Image) model.Resource {
	tags := ec2Tags(img.Tags)
	id := aws.ToString(img.ImageId)
	return resource(req, "AWS::EC2::Image", regionalARN(req, "ec2", "image/"+id),
		firstNonEmpty(aws.ToString(img.Name), id), parseTime(aws.ToString(img.CreationDate)), tags)
}

func snapshotResource(req listRequest, s ec2types.Snapshot) model.Resource {
	tags := ec2Tags(s.Tags)
	id := aws.ToString(s.SnapshotId)
	r := resource(req, "AWS::EC2::Snapshot", regionalARN(req, "ec2", "snapshot/"+id),
		firstNonEmpty(tagValue(tags, "Name"), id), s.StartTime, tags)
	r.Attrs = map[string]string{"encrypted": fmt.Sprint(aws.ToBool(s.Encrypted))}
	return r
}

func ec2Tags(in []ec2types.Tag) []model.Tag {
	out := make([]model.Tag, 0, len(in))
	for _, t := range in {
		out = append(out, model.Tag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value), Kind: model.KindTag})
	}
	return out
}

// ---------------------------------------------------------------- ECS

func listECS(ctx context.Context, cfg aws.Config, req listRequest) ([]model.Resource, error) {
	c := ecs.NewFromConfig(cfg)
	var out []model.Resource
	clusters := ecs.NewListClustersPaginator(c, &ecs.ListClustersInput{})
	for clusters.HasMorePages() {
		page, err := clusters.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("ListClusters: %w", err)
		}
		for _, cluster := range page.ClusterArns {
			tasks := ecs.NewListTasksPaginator(c, &ecs.ListTasksInput{Cluster: aws.String(cluster)})
			for tasks.HasMorePages() {
				tp, err := tasks.NextPage(ctx)
				if err != nil {
					return nil, fmt.Errorf("ListTasks: %w", err)
				}
				if len(tp.TaskArns) == 0 {
					continue
				}
				// ListTasks pages hold at most 100 ARNs, DescribeTasks' limit.
				desc, err := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{
					Cluster: aws.String(cluster), Tasks: tp.TaskArns, Include: []ecstypes.TaskField{ecstypes.TaskFieldTags},
				})
				if err != nil {
					return nil, fmt.Errorf("DescribeTasks: %w", err)
				}
				for _, t := range desc.Tasks {
					out = append(out, taskResource(req, t))
				}
			}
		}
	}
	return out, nil
}

func taskResource(req listRequest, t ecstypes.Task) model.Resource {
	tags := make([]model.Tag, 0, len(t.Tags))
	for _, tg := range t.Tags {
		tags = append(tags, model.Tag{Key: aws.ToString(tg.Key), Value: aws.ToString(tg.Value), Kind: model.KindTag})
	}
	arn := aws.ToString(t.TaskArn)
	return resource(req, "AWS::ECS::Task", arn, arn[strings.LastIndex(arn, "/")+1:], t.CreatedAt, tags)
}

// ---------------------------------------------------------------- ElastiCache

func listElastiCache(ctx context.Context, cfg aws.Config, req listRequest) ([]model.Resource, error) {
	c := elasticache.NewFromConfig(cfg)
	tagsOf := func(arn string) ([]model.Tag, error) {
		out, err := c.ListTagsForResource(ctx, &elasticache.ListTagsForResourceInput{ResourceName: aws.String(arn)})
		if err != nil {
			return nil, fmt.Errorf("ListTagsForResource %s: %w", arn, err)
		}
		tags := make([]model.Tag, 0, len(out.TagList))
		for _, t := range out.TagList {
			tags = append(tags, model.Tag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value), Kind: model.KindTag})
		}
		return tags, nil
	}
	var out []model.Resource
	if req.wants("AWS::ElastiCache::CacheCluster") {
		pg := elasticache.NewDescribeCacheClustersPaginator(c, &elasticache.DescribeCacheClustersInput{})
		for pg.HasMorePages() {
			page, err := pg.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("DescribeCacheClusters: %w", err)
			}
			for _, cc := range page.CacheClusters {
				tags, err := tagsOf(aws.ToString(cc.ARN))
				if err != nil {
					return nil, err
				}
				r := resource(req, "AWS::ElastiCache::CacheCluster", aws.ToString(cc.ARN), aws.ToString(cc.CacheClusterId), cc.CacheClusterCreateTime, tags)
				r.Attrs = map[string]string{"engine": aws.ToString(cc.Engine)}
				out = append(out, r)
			}
		}
	}
	if req.wants("AWS::ElastiCache::ReplicationGroup") {
		pg := elasticache.NewDescribeReplicationGroupsPaginator(c, &elasticache.DescribeReplicationGroupsInput{})
		for pg.HasMorePages() {
			page, err := pg.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("DescribeReplicationGroups: %w", err)
			}
			for _, rg := range page.ReplicationGroups {
				tags, err := tagsOf(aws.ToString(rg.ARN))
				if err != nil {
					return nil, err
				}
				r := resource(req, "AWS::ElastiCache::ReplicationGroup", aws.ToString(rg.ARN), aws.ToString(rg.ReplicationGroupId), rg.ReplicationGroupCreateTime, tags)
				r.Attrs = map[string]string{"engine": aws.ToString(rg.Engine)}
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- FSx

func listFSx(ctx context.Context, cfg aws.Config, req listRequest) ([]model.Resource, error) {
	c := fsx.NewFromConfig(cfg)
	var out []model.Resource
	if req.wants("AWS::FSx::FileSystem") {
		pg := fsx.NewDescribeFileSystemsPaginator(c, &fsx.DescribeFileSystemsInput{})
		for pg.HasMorePages() {
			page, err := pg.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("DescribeFileSystems: %w", err)
			}
			for _, fs := range page.FileSystems {
				out = append(out, fileSystemResource(req, fs))
			}
		}
	}
	if req.wants("AWS::FSx::FileCache") {
		pg := fsx.NewDescribeFileCachesPaginator(c, &fsx.DescribeFileCachesInput{})
		for pg.HasMorePages() {
			page, err := pg.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("DescribeFileCaches: %w", err)
			}
			for _, fc := range page.FileCaches {
				arn := aws.ToString(fc.ResourceARN)
				tagOut, err := c.ListTagsForResource(ctx, &fsx.ListTagsForResourceInput{ResourceARN: aws.String(arn)})
				if err != nil {
					return nil, fmt.Errorf("ListTagsForResource %s: %w", arn, err)
				}
				out = append(out, resource(req, "AWS::FSx::FileCache", arn, aws.ToString(fc.FileCacheId), fc.CreationTime, fsxTags(tagOut.Tags)))
			}
		}
	}
	return out, nil
}

func fileSystemResource(req listRequest, fs fsxtypes.FileSystem) model.Resource {
	tags := fsxTags(fs.Tags)
	id := aws.ToString(fs.FileSystemId)
	r := resource(req, "AWS::FSx::FileSystem", aws.ToString(fs.ResourceARN), firstNonEmpty(tagValue(tags, "Name"), id), fs.CreationTime, tags)
	r.Attrs = map[string]string{"file_system_type": string(fs.FileSystemType)}
	return r
}

func fsxTags(in []fsxtypes.Tag) []model.Tag {
	out := make([]model.Tag, 0, len(in))
	for _, t := range in {
		out = append(out, model.Tag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value), Kind: model.KindTag})
	}
	return out
}

// ---------------------------------------------------------------- CloudWatch Logs

func listLogs(ctx context.Context, cfg aws.Config, req listRequest) ([]model.Resource, error) {
	c := cloudwatchlogs.NewFromConfig(cfg)
	var out []model.Resource
	pg := cloudwatchlogs.NewDescribeLogGroupsPaginator(c, &cloudwatchlogs.DescribeLogGroupsInput{})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("DescribeLogGroups: %w", err)
		}
		for _, g := range page.LogGroups {
			// LogGroupArn has no trailing ":*", which is the form tag APIs take.
			arn := firstNonEmpty(aws.ToString(g.LogGroupArn), strings.TrimSuffix(aws.ToString(g.Arn), ":*"))
			tagOut, err := c.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
			if err != nil {
				return nil, fmt.Errorf("ListTagsForResource %s: %w", arn, err)
			}
			out = append(out, resource(req, "AWS::Logs::LogGroup", arn, aws.ToString(g.LogGroupName), millis(g.CreationTime), mapTags(tagOut.Tags)))
		}
	}
	return out, nil
}

func millis(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	return &t
}

func mapTags(m map[string]string) []model.Tag {
	out := make([]model.Tag, 0, len(m))
	for k, v := range m {
		out = append(out, model.Tag{Key: k, Value: v, Kind: model.KindTag})
	}
	return out
}

// ---------------------------------------------------------------- SSM

func listSSM(ctx context.Context, cfg aws.Config, req listRequest) ([]model.Resource, error) {
	c := ssm.NewFromConfig(cfg)
	var out []model.Resource
	pg := ssm.NewDescribeParametersPaginator(c, &ssm.DescribeParametersInput{})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("DescribeParameters: %w", err)
		}
		for _, p := range page.Parameters {
			name := aws.ToString(p.Name)
			tagOut, err := c.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{
				ResourceType: ssmtypes.ResourceTypeForTaggingParameter, ResourceId: aws.String(name),
			})
			if err != nil {
				return nil, fmt.Errorf("ListTagsForResource %s: %w", name, err)
			}
			tags := make([]model.Tag, 0, len(tagOut.TagList))
			for _, t := range tagOut.TagList {
				tags = append(tags, model.Tag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value), Kind: model.KindTag})
			}
			arn := firstNonEmpty(aws.ToString(p.ARN), regionalARN(req, "ssm", "parameter/"+strings.TrimPrefix(name, "/")))
			// SSM keeps no creation time; LastModifiedDate would wrongly
			// skip old parameters edited inside the grace period.
			out = append(out, resource(req, "AWS::SSM::Parameter", arn, name, nil, tags))
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- SageMaker

func listSageMaker(ctx context.Context, cfg aws.Config, req listRequest) ([]model.Resource, error) {
	c := sagemaker.NewFromConfig(cfg)
	var out []model.Resource
	pg := sagemaker.NewListNotebookInstancesPaginator(c, &sagemaker.ListNotebookInstancesInput{})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("ListNotebookInstances: %w", err)
		}
		for _, nb := range page.NotebookInstances {
			arn := aws.ToString(nb.NotebookInstanceArn)
			var tags []model.Tag
			tp := sagemaker.NewListTagsPaginator(c, &sagemaker.ListTagsInput{ResourceArn: aws.String(arn)})
			for tp.HasMorePages() {
				tagPage, err := tp.NextPage(ctx)
				if err != nil {
					return nil, fmt.Errorf("ListTags %s: %w", arn, err)
				}
				for _, t := range tagPage.Tags {
					tags = append(tags, model.Tag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value), Kind: model.KindTag})
				}
			}
			out = append(out, resource(req, "AWS::SageMaker::NotebookInstance", arn, aws.ToString(nb.NotebookInstanceName), nb.CreationTime, tags))
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- Lightsail

func listLightsail(ctx context.Context, cfg aws.Config, req listRequest) ([]model.Resource, error) {
	c := lightsail.NewFromConfig(cfg)
	var out []model.Resource
	in := &lightsail.GetInstancesInput{}
	for {
		page, err := c.GetInstances(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("GetInstances: %w", err)
		}
		for _, i := range page.Instances {
			tags := make([]model.Tag, 0, len(i.Tags))
			for _, t := range i.Tags {
				tags = append(tags, model.Tag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value), Kind: model.KindTag})
			}
			out = append(out, resource(req, "AWS::Lightsail::Instance", aws.ToString(i.Arn), aws.ToString(i.Name), i.CreatedAt, tags))
		}
		if aws.ToString(page.NextPageToken) == "" {
			return out, nil
		}
		in.PageToken = page.NextPageToken
	}
}

// ---------------------------------------------------------------- Organizations

// orgAPI is the part of the Organizations client the adapter uses.
type orgAPI interface {
	organizations.ListTagsForResourceAPIClient
	organizations.ListPoliciesAPIClient
}

// listOrganizations returns the scope's own account resource and, in the
// management account, the organization's SCPs and RCPs.
func listOrganizations(ctx context.Context, c orgAPI, limit *rate.Limiter, req listRequest) ([]model.Resource, error) {
	var out []model.Resource
	if req.wants("AWS::Organizations::Account") {
		tags, err := orgTags(ctx, c, limit, req.Account)
		if err != nil {
			return nil, fmt.Errorf("account tags: %w", err)
		}
		arn := firstNonEmpty(req.AccountARN, fmt.Sprintf("arn:%s:organizations::%s:account/%s", req.Partition, req.Account, req.Account))
		out = append(out, resource(req, "AWS::Organizations::Account", arn, firstNonEmpty(req.AccountName, req.Account), nil, tags))
	}
	if !req.wants("AWS::Organizations::Policy") || !req.Management {
		return out, nil
	}
	for _, typ := range []orgtypes.PolicyType{orgtypes.PolicyTypeServiceControlPolicy, orgtypes.PolicyTypeResourceControlPolicy} {
		pg := organizations.NewListPoliciesPaginator(c, &organizations.ListPoliciesInput{Filter: typ})
		for pg.HasMorePages() {
			if err := limit.Wait(ctx); err != nil {
				return nil, err
			}
			page, err := pg.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("ListPolicies %s: %w", typ, err)
			}
			for _, p := range page.Policies {
				if p.AwsManaged {
					continue // AWS managed policies such as FullAWSAccess cannot be tagged
				}
				tags, err := orgTags(ctx, c, limit, aws.ToString(p.Id))
				if err != nil {
					return nil, fmt.Errorf("policy %s tags: %w", aws.ToString(p.Id), err)
				}
				r := resource(req, "AWS::Organizations::Policy", aws.ToString(p.Arn), aws.ToString(p.Name), nil, tags)
				r.Attrs = map[string]string{"policy_type": string(typ)}
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func orgTags(ctx context.Context, c organizations.ListTagsForResourceAPIClient, limit *rate.Limiter, id string) ([]model.Tag, error) {
	var tags []model.Tag
	pg := organizations.NewListTagsForResourcePaginator(c, &organizations.ListTagsForResourceInput{ResourceId: aws.String(id)})
	for pg.HasMorePages() {
		if err := limit.Wait(ctx); err != nil {
			return nil, err
		}
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range page.Tags {
			tags = append(tags, model.Tag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value), Kind: model.KindTag})
		}
	}
	return tags, nil
}
