package gcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	asset "cloud.google.com/go/asset/apiv1"
	"cloud.google.com/go/asset/apiv1/assetpb"
	"github.com/googleapis/gax-go/v2"
	"golang.org/x/time/rate"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// pageSize is SearchAllResources' maximum page size.
const pageSize = 500

// sdkBackend implements backend with the Cloud Asset Inventory client.
type sdkBackend struct {
	client *asset.Client
	// SearchAllResources is limited per consumer project; every scope
	// shares this limiter.
	limit *rate.Limiter
}

func newSDKBackend(ctx context.Context, opts Options) (*sdkBackend, error) {
	var clientOpts []option.ClientOption
	if f := opts.Scopes.CredentialsFile; f != "" {
		clientOpts = append(clientOpts, option.WithAuthCredentialsFile(option.ServiceAccount, f))
	}
	c, err := asset.NewClient(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("GCP credentials: %w", err)
	}
	return &sdkBackend{client: c, limit: rate.NewLimiter(5, 5)}, nil
}

// retry backs off on throttling and transient errors, with jitter.
var retry = gax.WithRetry(func() gax.Retryer {
	return gax.OnCodes([]codes.Code{codes.ResourceExhausted, codes.Unavailable, codes.DeadlineExceeded}, gax.Backoff{
		Initial: time.Second, Max: 30 * time.Second, Multiplier: 2,
	})
})

func (b *sdkBackend) Search(ctx context.Context, scope string, assetTypes []string, query string, mask []string) ([]*assetpb.ResourceSearchResult, error) {
	req := &assetpb.SearchAllResourcesRequest{
		Scope: scope, AssetTypes: assetTypes, Query: query, PageSize: pageSize,
		ReadMask: &fieldmaskpb.FieldMask{Paths: mask},
	}
	it := b.client.SearchAllResources(ctx, req, retry, gax.WithTimeout(searchTimeout))
	var out []*assetpb.ResourceSearchResult
	for {
		// The iterator fetches a page when its buffer is empty.
		if it.PageInfo().Remaining() == 0 {
			if err := b.limit.Wait(ctx); err != nil {
				return nil, err
			}
		}
		r, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
}
