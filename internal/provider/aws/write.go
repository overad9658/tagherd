package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

// Batch sizes of the Resource Groups Tagging API.
const (
	tagBatch  = 20  // TagResources ARNs per call
	readBatch = 100 // GetResources ARNs per call
)

// Services with their own tag calls; everything else uses the Resource
// Groups Tagging API.
const (
	svcTagging       = "tagging"
	svcIAM           = "iam"
	svcOrganizations = "organizations"
)

// iamTypes are tagged through IAM's own calls.
var iamTypes = []string{
	"AWS::IAM::Role", "AWS::IAM::User", "AWS::IAM::Policy", "AWS::IAM::ServerCertificate",
	"AWS::IAM::OIDCProvider", "AWS::IAM::SAMLProvider",
}

// Writer returns a tag writer with the write role in the account. It fails
// when the role cannot be assumed.
func (p *Provider) Writer(ctx context.Context, s provider.Scope) (provider.TagWriter, error) {
	actor, err := p.b.WriteCaller(ctx, s.ID)
	if err != nil {
		if role := p.opts.Scopes.WriteRoleName; role != "" {
			return nil, fmt.Errorf("assuming %s in %s: %w", role, s.ID, err)
		}
		return nil, err
	}
	return &writer{p: p, account: s.ID, actor: actor}, nil
}

type writer struct {
	p       *Provider
	account string
	actor   string
}

func (w *writer) Actor(context.Context) (string, error) { return w.actor, nil }

// route says which API writes a target, in which account and region.
type route struct {
	service, account, region string
}

func (w *writer) route(ctx context.Context, t provider.Target) (route, error) {
	switch {
	case strings.HasPrefix(t.NativeType, "AWS::Organizations::"):
		// Organizations resources are tagged from the management account.
		mgmt := w.p.management(ctx)
		if mgmt == "" {
			return route{}, errors.New("the Organizations management account is unknown, so organization resources cannot be tagged")
		}
		return route{service: svcOrganizations, account: mgmt}, nil
	case slices.Contains(iamTypes, t.NativeType):
		return route{service: svcIAM, account: w.account}, nil
	}
	a, err := arn.Parse(t.ID)
	if err != nil {
		return route{}, fmt.Errorf("%s has no ARN, so it cannot be tagged", t.ID)
	}
	region := firstNonEmpty(t.Region, a.Region, defaultRegion)
	return route{service: svcTagging, account: w.account, region: region}, nil
}

func (w *writer) Read(ctx context.Context, targets []provider.Target) (map[string][]model.Tag, map[string]error) {
	out, errs := map[string][]model.Tag{}, map[string]error{}
	byRegion := map[string][]string{}
	for _, t := range targets {
		rt, err := w.route(ctx, t)
		if err != nil {
			errs[t.ID] = err
			continue
		}
		if rt.service != svcTagging {
			// Organizations resources are read with the base credentials,
			// like discovery.
			tags, err := w.p.b.ServiceTags(ctx, rt.account, rt.service, t.ID)
			if err != nil {
				errs[t.ID] = err
			} else {
				out[t.ID] = nonNilTags(tags)
			}
			continue
		}
		byRegion[rt.region] = append(byRegion[rt.region], t.ID)
	}
	for _, region := range sortedKeys(byRegion) {
		for chunk := range slices.Chunk(byRegion[region], readBatch) {
			got, err := w.p.b.TaggedResources(ctx, w.account, region, chunk)
			for _, id := range chunk {
				switch {
				case err != nil:
					errs[id] = err
				default:
					// GetResources omits resources that never had a tag.
					out[id] = nonNilTags(got[id])
				}
			}
		}
	}
	return out, errs
}

func (w *writer) Write(ctx context.Context, writes []provider.TagWrite) map[string]error {
	errs := map[string]error{}
	type group struct {
		region string
		tags   map[string]string
		ids    []string
	}
	groups := map[string]*group{}
	var order []string
	for _, wr := range writes {
		tags := map[string]string{}
		for _, t := range wr.Tags {
			if reservedKey(t.Key) {
				errs[wr.Target.ID] = fmt.Errorf("%s uses the reserved aws: prefix", t.Key)
				break
			}
			tags[t.Key] = t.Value
		}
		if errs[wr.Target.ID] != nil {
			continue
		}
		rt, err := w.route(ctx, wr.Target)
		if err != nil {
			errs[wr.Target.ID] = err
			continue
		}
		if rt.service != svcTagging {
			if err := w.p.b.TagService(ctx, rt.account, rt.service, wr.Target.ID, tags); err != nil {
				errs[wr.Target.ID] = err
			}
			continue
		}
		// Resources getting the same tags share TagResources calls.
		key := rt.region + "\x00" + tagKey(tags)
		if groups[key] == nil {
			groups[key] = &group{region: rt.region, tags: tags}
			order = append(order, key)
		}
		groups[key].ids = append(groups[key].ids, wr.Target.ID)
	}
	for _, key := range order {
		g := groups[key]
		for chunk := range slices.Chunk(g.ids, tagBatch) {
			failed, err := w.p.b.TagResources(ctx, w.account, g.region, chunk, g.tags)
			for _, id := range chunk {
				if err != nil {
					errs[id] = err
				} else if f := failed[id]; f != nil {
					errs[id] = f
				}
			}
		}
	}
	return errs
}

func tagKey(tags map[string]string) string {
	var b strings.Builder
	for _, k := range sortedKeys(tags) {
		b.WriteString(k + "\x00" + tags[k] + "\x00")
	}
	return b.String()
}

func reservedKey(k string) bool {
	return len(k) >= 4 && strings.EqualFold(k[:4], "aws:")
}

func nonNilTags(t []model.Tag) []model.Tag {
	if t == nil {
		return []model.Tag{}
	}
	return t
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
