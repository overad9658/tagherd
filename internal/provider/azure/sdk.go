package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resourcegraph/armresourcegraph"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"golang.org/x/time/rate"
)

// pageSize is Resource Graph's maximum rows per page.
const pageSize = 1000

// sdkBackend implements backend with the Azure SDK.
type sdkBackend struct {
	cred  azcore.TokenCredential
	subs  *armsubscriptions.Client
	graph *armresourcegraph.Client
	tags  *armresources.TagsClient
	// Resource Graph allows 15 queries per 5 seconds per user; every scope
	// shares this limiter.
	limit *rate.Limiter
	// Tag writes are rate-limited client-side to stay well under ARM's
	// write throttling.
	writeLimit *rate.Limiter
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
	tags, err := armresources.NewTagsClient("", cred, clientOpts)
	if err != nil {
		return nil, err
	}
	return &sdkBackend{cred: cred, subs: subs, graph: graph, tags: tags, limit: rate.NewLimiter(2.5, 5), writeLimit: rate.NewLimiter(5, 5)}, nil
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

// Caller reads the object ID (and user or app name) from an ARM token.
func (b *sdkBackend) Caller(ctx context.Context) (string, error) {
	tok, err := b.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://management.azure.com/.default"}})
	if err != nil {
		return "", err
	}
	return tokenActor(tok.Token)
}

// tokenActor names the identity in a JWT: "upn (oid)", or the oid alone.
func tokenActor(jwt string) (string, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("unexpected token format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		OID   string `json:"oid"`
		UPN   string `json:"upn"`
		AppID string `json:"appid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if claims.OID == "" {
		return "", fmt.Errorf("token has no oid claim")
	}
	if name := firstNonEmpty(claims.UPN, claims.AppID); name != "" {
		return fmt.Sprintf("%s (oid %s)", name, claims.OID), nil
	}
	return "oid " + claims.OID, nil
}

func (b *sdkBackend) Tags(ctx context.Context, scope string) (map[string]string, error) {
	resp, err := b.tags.GetAtScope(ctx, scope, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if resp.Properties != nil {
		for k, v := range resp.Properties.Tags {
			out[k] = str(v)
		}
	}
	return out, nil
}

func (b *sdkBackend) MergeTags(ctx context.Context, scope string, tags map[string]string) error {
	if err := b.writeLimit.Wait(ctx); err != nil {
		return err
	}
	props := &armresources.Tags{Tags: map[string]*string{}}
	for k, v := range tags {
		props.Tags[k] = to.Ptr(v)
	}
	_, err := b.tags.UpdateAtScope(ctx, scope, armresources.TagsPatchResource{
		Operation: to.Ptr(armresources.TagsPatchOperationMerge), Properties: props,
	}, nil)
	return err
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
