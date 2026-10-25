package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	tagging "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/overad9658/tagherd/internal/model"
)

func (b *sdkBackend) WriteCaller(ctx context.Context, account string) (string, error) {
	cfg, err := b.writer(ctx, account, "")
	if err != nil {
		return "", err
	}
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.Arn), nil
}

func (b *sdkBackend) TaggedResources(ctx context.Context, account, region string, arns []string) (map[string][]model.Tag, error) {
	// Reads use the write role too, so the precondition sees what the
	// write will see.
	cfg, err := b.writer(ctx, account, region)
	if err != nil {
		return nil, err
	}
	out := map[string][]model.Tag{}
	pg := tagging.NewGetResourcesPaginator(tagging.NewFromConfig(cfg), &tagging.GetResourcesInput{ResourceARNList: arns})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, m := range page.ResourceTagMappingList {
			tags := make([]model.Tag, 0, len(m.Tags))
			for _, t := range m.Tags {
				tags = append(tags, model.Tag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value), Kind: model.KindTag})
			}
			out[aws.ToString(m.ResourceARN)] = tags
		}
	}
	return out, nil
}

func (b *sdkBackend) TagResources(ctx context.Context, account, region string, arns []string, tags map[string]string) (map[string]error, error) {
	cfg, err := b.writer(ctx, account, region)
	if err != nil {
		return nil, err
	}
	out, err := tagging.NewFromConfig(cfg).TagResources(ctx, &tagging.TagResourcesInput{ResourceARNList: arns, Tags: tags})
	if err != nil {
		return nil, err
	}
	failed := map[string]error{}
	for id, f := range out.FailedResourcesMap {
		failed[id] = fmt.Errorf("%s: %s", f.ErrorCode, aws.ToString(f.ErrorMessage))
	}
	return failed, nil
}

func (b *sdkBackend) ServiceTags(ctx context.Context, account, service, id string) ([]model.Tag, error) {
	switch service {
	case svcOrganizations:
		return orgTags(ctx, organizations.NewFromConfig(b.base), b.orgLimit, lastPathSegment(id))
	case svcIAM:
		cfg, err := b.writer(ctx, account, defaultRegion)
		if err != nil {
			return nil, err
		}
		return iamTags(ctx, iam.NewFromConfig(cfg), id)
	}
	return nil, fmt.Errorf("no tag reader for %s", service)
}

func (b *sdkBackend) TagService(ctx context.Context, account, service, id string, tags map[string]string) error {
	switch service {
	case svcOrganizations:
		cfg, err := b.writer(ctx, account, defaultRegion)
		if err != nil {
			return err
		}
		var ts []orgtypes.Tag
		for _, k := range sortedKeys(tags) {
			ts = append(ts, orgtypes.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
		}
		if err := b.orgLimit.Wait(ctx); err != nil {
			return err
		}
		_, err = organizations.NewFromConfig(cfg).TagResource(ctx, &organizations.TagResourceInput{ResourceId: aws.String(lastPathSegment(id)), Tags: ts})
		return err
	case svcIAM:
		cfg, err := b.writer(ctx, account, defaultRegion)
		if err != nil {
			return err
		}
		return tagIAM(ctx, iam.NewFromConfig(cfg), id, tags)
	}
	return fmt.Errorf("no tag writer for %s", service)
}

// lastPathSegment is the ID at the end of an ARN: account, OU or policy ID,
// or an IAM name.
func lastPathSegment(id string) string { return id[strings.LastIndex(id, "/")+1:] }

// iamResource splits an IAM ARN into its resource kind (role, user, ...).
func iamResource(id string) (string, error) {
	a, err := arn.Parse(id)
	if err != nil {
		return "", fmt.Errorf("%s is not an IAM ARN", id)
	}
	kind, _, _ := strings.Cut(a.Resource, "/")
	return kind, nil
}

func iamTags(ctx context.Context, c *iam.Client, id string) ([]model.Tag, error) {
	kind, err := iamResource(id)
	if err != nil {
		return nil, err
	}
	var tags []iamtypes.Tag
	name := lastPathSegment(id)
	switch kind {
	case "role":
		out, err := c.ListRoleTags(ctx, &iam.ListRoleTagsInput{RoleName: aws.String(name)})
		if err != nil {
			return nil, err
		}
		tags = out.Tags
	case "user":
		out, err := c.ListUserTags(ctx, &iam.ListUserTagsInput{UserName: aws.String(name)})
		if err != nil {
			return nil, err
		}
		tags = out.Tags
	case "policy":
		out, err := c.ListPolicyTags(ctx, &iam.ListPolicyTagsInput{PolicyArn: aws.String(id)})
		if err != nil {
			return nil, err
		}
		tags = out.Tags
	case "server-certificate":
		out, err := c.ListServerCertificateTags(ctx, &iam.ListServerCertificateTagsInput{ServerCertificateName: aws.String(name)})
		if err != nil {
			return nil, err
		}
		tags = out.Tags
	case "oidc-provider":
		out, err := c.ListOpenIDConnectProviderTags(ctx, &iam.ListOpenIDConnectProviderTagsInput{OpenIDConnectProviderArn: aws.String(id)})
		if err != nil {
			return nil, err
		}
		tags = out.Tags
	case "saml-provider":
		out, err := c.ListSAMLProviderTags(ctx, &iam.ListSAMLProviderTagsInput{SAMLProviderArn: aws.String(id)})
		if err != nil {
			return nil, err
		}
		tags = out.Tags
	default:
		return nil, fmt.Errorf("no IAM tag reader for %s", kind)
	}
	out := make([]model.Tag, 0, len(tags))
	for _, t := range tags {
		out = append(out, model.Tag{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value), Kind: model.KindTag})
	}
	return out, nil
}

func tagIAM(ctx context.Context, c *iam.Client, id string, tags map[string]string) error {
	kind, err := iamResource(id)
	if err != nil {
		return err
	}
	var ts []iamtypes.Tag
	for _, k := range sortedKeys(tags) {
		ts = append(ts, iamtypes.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	name := lastPathSegment(id)
	switch kind {
	case "role":
		_, err = c.TagRole(ctx, &iam.TagRoleInput{RoleName: aws.String(name), Tags: ts})
	case "user":
		_, err = c.TagUser(ctx, &iam.TagUserInput{UserName: aws.String(name), Tags: ts})
	case "policy":
		_, err = c.TagPolicy(ctx, &iam.TagPolicyInput{PolicyArn: aws.String(id), Tags: ts})
	case "server-certificate":
		_, err = c.TagServerCertificate(ctx, &iam.TagServerCertificateInput{ServerCertificateName: aws.String(name), Tags: ts})
	case "oidc-provider":
		_, err = c.TagOpenIDConnectProvider(ctx, &iam.TagOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(id), Tags: ts})
	case "saml-provider":
		_, err = c.TagSAMLProvider(ctx, &iam.TagSAMLProviderInput{SAMLProviderArn: aws.String(id), Tags: ts})
	default:
		err = errors.New("no IAM tag writer for " + kind)
	}
	return err
}
