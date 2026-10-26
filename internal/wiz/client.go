package wiz

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/overad9658/tagherd/internal/provider"
)

// Defaults for the Wiz connection.
const (
	DefaultAuthURL = "https://auth.app.wiz.io/oauth/token"
	DefaultTimeout = 60 * time.Second
	pageSize       = 500
	maxPages       = 1000
)

// graphSearch is the Wiz graph query; the override's missing block is its
// query variable, unchanged.
const graphSearch = `query GraphSearch($query: GraphEntityQueryInput, $projectId: String!, $first: Int, $after: String, $quick: Boolean) {
  graphSearch(query: $query, projectId: $projectId, first: $first, after: $after, quick: $quick) {
    maxCountReached
    pageInfo { endCursor hasNextPage }
    nodes { entities { id type properties } }
  }
}`

// ErrIncomplete means Wiz capped the result set. A partial list would
// exempt every AMI left out of it, so it is never used.
var ErrIncomplete = errors.New("wiz: more results than it returns (maxCountReached); narrow the query")

// Options configure a Client.
type Options struct {
	APIURL       string // the tenant's GraphQL endpoint, https://api.<region>.app.wiz.io/graphql
	AuthURL      string // default DefaultAuthURL
	ClientID     string
	ClientSecret string
	Timeout      time.Duration // per query, every page included; default DefaultTimeout
	HTTP         *http.Client
	// Backoff is the wait before retry n (1, 2, ...) of a throttled or
	// failed call; default n seconds.
	Backoff func(n int) time.Duration
}

// Client runs graph queries with OAuth client credentials. Results are
// cached by query for the life of the client, so a scan asks once however
// many accounts it covers.
type Client struct {
	opts Options

	mu      sync.Mutex
	token   string
	expires time.Time
	cache   map[string]map[string]bool
}

// NewClient checks the options and returns a client.
func NewClient(opts Options) (*Client, error) {
	switch {
	case opts.APIURL == "":
		return nil, errors.New("wiz.api_url is not set")
	case opts.ClientID == "" || opts.ClientSecret == "":
		return nil, errors.New("wiz.client_id and wiz.client_secret are required")
	}
	opts.AuthURL = cmp.Or(opts.AuthURL, DefaultAuthURL)
	opts.Timeout = cmp.Or(opts.Timeout, DefaultTimeout)
	if opts.HTTP == nil {
		opts.HTTP = http.DefaultClient
	}
	if opts.Backoff == nil {
		opts.Backoff = func(n int) time.Duration { return time.Duration(n) * time.Second }
	}
	return &Client{opts: opts, cache: map[string]map[string]bool{}}, nil
}

// Search runs q and returns the identifiers of every entity it selects:
// each entity's providerUniqueId and externalId, as Wiz reports them.
func (c *Client) Search(ctx context.Context, q *Query) (map[string]bool, error) {
	key := string(q.Raw)
	c.mu.Lock()
	if ids, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return ids, nil
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	ids := map[string]bool{}
	var after *string
	for range maxPages {
		vars := map[string]any{"query": q.Raw, "projectId": "*", "first": pageSize, "quick": true}
		if after != nil {
			vars["after"] = *after
		}
		var page struct {
			GraphSearch struct {
				MaxCountReached bool `json:"maxCountReached"`
				PageInfo        struct {
					EndCursor   string `json:"endCursor"`
					HasNextPage bool   `json:"hasNextPage"`
				} `json:"pageInfo"`
				Nodes []struct {
					Entities []struct {
						ID         string         `json:"id"`
						Type       string         `json:"type"`
						Properties map[string]any `json:"properties"`
					} `json:"entities"`
				} `json:"nodes"`
			} `json:"graphSearch"`
		}
		if err := c.graphql(ctx, graphSearch, vars, &page); err != nil {
			return nil, err
		}
		gs := page.GraphSearch
		if gs.MaxCountReached {
			return nil, ErrIncomplete
		}
		for _, n := range gs.Nodes {
			for _, e := range n.Entities {
				for _, prop := range []string{"providerUniqueId", "externalId"} {
					if s, _ := e.Properties[prop].(string); s != "" {
						ids[s] = true
					}
				}
			}
		}
		if !gs.PageInfo.HasNextPage {
			c.mu.Lock()
			c.cache[key] = ids
			c.mu.Unlock()
			return ids, nil
		}
		if gs.PageInfo.EndCursor == "" {
			return nil, errors.New("wiz: hasNextPage without an endCursor")
		}
		cursor := gs.PageInfo.EndCursor
		after = &cursor
	}
	return nil, fmt.Errorf("wiz: more than %d pages", maxPages)
}

// Run is Search for the scan's override hook. The query is sent unchanged,
// so it is not narrowed to the scopes; the scan joins the result to the
// resources it discovered.
func (c *Client) Run(ctx context.Context, q *Query, _ []provider.Scope) (map[string]bool, error) {
	return c.Search(ctx, q)
}

// graphql posts one GraphQL call, retrying throttling and server errors,
// and decodes data into out.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.APIURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		status, data, err := c.do(req)
		retry := err != nil || status == http.StatusTooManyRequests || status >= 500
		if status == http.StatusUnauthorized && !refreshed {
			c.mu.Lock()
			c.token = "" // expired early; get a new one, once
			c.mu.Unlock()
			refreshed, retry = true, true
		}
		if retry && attempt < 3 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("wiz: %w", ctx.Err())
			case <-time.After(c.opts.Backoff(attempt + 1)):
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("wiz: %w", err)
		}
		if status/100 != 2 {
			return fmt.Errorf("wiz: %d: %s", status, snippet(data))
		}
		var resp struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return fmt.Errorf("wiz: reading the response: %w", err)
		}
		if len(resp.Errors) > 0 {
			msgs := make([]string, len(resp.Errors))
			for i, e := range resp.Errors {
				msgs[i] = e.Message
			}
			return fmt.Errorf("wiz: %s", strings.Join(msgs, "; "))
		}
		return json.Unmarshal(resp.Data, out)
	}
}

// accessToken returns a cached token, or gets one with the client
// credentials grant.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"audience":      {"wiz-api"},
		"client_id":     {c.opts.ClientID},
		"client_secret": {c.opts.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.AuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	status, data, err := c.do(req)
	if err != nil {
		return "", fmt.Errorf("wiz auth: %w", err)
	}
	if status/100 != 2 {
		return "", fmt.Errorf("wiz auth: %d: %s", status, snippet(data))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("wiz auth: the response has no access_token")
	}
	c.token = tok.AccessToken
	// Renew a minute early.
	c.expires = time.Now().Add(time.Duration(max(tok.ExpiresIn-60, 0)) * time.Second)
	return c.token, nil
}

func (c *Client) do(req *http.Request) (int, []byte, error) {
	resp, err := c.opts.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, data, err
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	return s[:min(len(s), 300)]
}
