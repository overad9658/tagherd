package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resourcegraph/armresourcegraph"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"golang.org/x/time/rate"
)

// pageSize is Resource Graph's maximum rows per page.
const pageSize = 1000

// sdkBackend implements backend with the Azure SDK.
type sdkBackend struct {
	subs  *armsubscriptions.Client
	graph *armresourcegraph.Client
	// Resource Graph allows 15 queries per 5 seconds per user; every scope
	// shares this limiter.
	limit *rate.Limiter
}

func newSDKBackend(opts Options) (*sdkBackend, error) {
	cred, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{TenantID: opts.Scopes.TenantID})
	if err != nil {
		return nil, fmt.Errorf("loading Azure credentials: %w", err)
	}
	clientOpts := &arm.ClientOptions{ClientOptions: policy.ClientOptions{Retry: policy.RetryOptions{MaxRetries: 6}}}
	subs, err := armsubscriptions.NewClient(cred, clientOpts)
	if err != nil {
		return nil, err
	}
	graph, err := armresourcegraph.NewClient(cred, clientOpts)
	if err != nil {
		return nil, err
	}
	return &sdkBackend{subs: subs, graph: graph, limit: rate.NewLimiter(2.5, 5)}, nil
}

func (b *sdkBackend) Subscriptions(ctx context.Context) ([]subscription, error) {
	var out []subscription
	pg := b.subs.NewListPager(nil)
	for pg.More() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range page.Value {
			sub := subscription{ID: str(s.SubscriptionID), Name: str(s.DisplayName), Tenant: str(s.TenantID)}
			if s.State != nil {
				sub.State = string(*s.State)
			}
			out = append(out, sub)
		}
	}
	return out, nil
}

func (b *sdkBackend) Query(ctx context.Context, subscriptionID, query string) ([]map[string]any, error) {
	req := armresourcegraph.QueryRequest{
		Query:         to.Ptr(query),
		Subscriptions: []*string{to.Ptr(subscriptionID)},
		Options: &armresourcegraph.QueryRequestOptions{
			ResultFormat: to.Ptr(armresourcegraph.ResultFormatObjectArray),
			Top:          to.Ptr[int32](pageSize),
		},
	}
	var rows []map[string]any
	for {
		if err := b.limit.Wait(ctx); err != nil {
			return nil, err
		}
		resp, err := b.graph.Resources(ctx, req, nil)
		if err != nil {
			return nil, err
		}
		data, _ := resp.Data.([]any)
		for _, d := range data {
			if row, ok := d.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
		if str(resp.SkipToken) == "" {
			return rows, nil
		}
		req.Options.SkipToken = resp.SkipToken
	}
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
