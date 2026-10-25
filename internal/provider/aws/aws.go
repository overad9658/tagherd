// Package aws is the AWS discovery adapter. Accounts come from AWS
// Organizations, resources from AWS Config advanced queries (through an
// aggregator when one is configured), and per-service list calls cover the
// types Config does not record.
package aws

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

// Options configure the adapter.
type Options struct {
	Scopes config.AWSScopes
	// Profile is --profile; it overrides Scopes.Profile.
	Profile string
	// RegionWorkers is how many regions of one account are read in
	// parallel; default 4.
	RegionWorkers int
	Log           *slog.Logger
}

// Provider implements provider.Provider for AWS.
type Provider struct {
	opts Options
	b    backend

	mu       sync.Mutex
	caller   *identity
	mgmt     *string // management account ID, once looked up
	tags     map[string][]model.Tag
	accounts map[string]account
}

var _ provider.Provider = (*Provider)(nil)

// identity is the base credential's caller.
type identity struct {
	Account   string
	Partition string
}

// account is one AWS Organizations member account.
type account struct {
	ID, Name, ARN string
}

// listRequest is one per-service list call: one account, one region (empty
// for global services), the native types wanted.
type listRequest struct {
	Account     string
	Region      string
	Partition   string
	NativeTypes []string
	// Management is set when Account is the Organizations management account.
	Management bool
	// AccountARN and AccountName describe Account, when Organizations knows it.
	AccountARN, AccountName string
}

func (r listRequest) wants(native string) bool { return slices.Contains(r.NativeTypes, native) }

// backend is the AWS API surface the adapter uses. sdkBackend implements it
// with the AWS SDK; tests use a stub.
type backend interface {
	Caller(ctx context.Context) (identity, error)
	// Accounts lists the organization's active accounts.
	Accounts(ctx context.Context) ([]account, error)
	ManagementAccount(ctx context.Context) (string, error)
	// Prepare checks that the read role can be assumed in an account.
	Prepare(ctx context.Context, account string) error
	Regions(ctx context.Context, account string) ([]string, error)
	SelectAggregate(ctx context.Context, agg config.AWSAggregator, expr string) ([]string, error)
	Select(ctx context.Context, account, region, expr string) ([]string, error)
	List(ctx context.Context, adapter string, req listRequest) ([]model.Resource, error)
	AccountTags(ctx context.Context, account string) ([]model.Tag, error)

	// WriteCaller assumes the write role in an account and returns its ARN.
	WriteCaller(ctx context.Context, account string) (string, error)
	// TaggedResources reads tags through the Resource Groups Tagging API.
	// ARNs it does not return have never had a tag.
	TaggedResources(ctx context.Context, account, region string, arns []string) (map[string][]model.Tag, error)
	// TagResources writes the same tags to up to 20 ARNs and returns the
	// ones that failed.
	TagResources(ctx context.Context, account, region string, arns []string, tags map[string]string) (map[string]error, error)
	// ServiceTags and TagService read and write through IAM or
	// Organizations, for types the Tagging API does not cover.
	ServiceTags(ctx context.Context, account, service, id string) ([]model.Tag, error)
	TagService(ctx context.Context, account, service, id string, tags map[string]string) error
}

// New returns the adapter with SDK clients from the standard credential
// chain, or the configured profile.
func New(ctx context.Context, opts Options) (*Provider, error) {
	b, err := newSDKBackend(ctx, opts)
	if err != nil {
		return nil, err
	}
	return newProvider(opts, b), nil
}

func newProvider(opts Options, b backend) *Provider {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.RegionWorkers < 1 {
		opts.RegionWorkers = 4
	}
	return &Provider{opts: opts, b: b, tags: map[string][]model.Tag{}, accounts: map[string]account{}}
}

func (p *Provider) Cloud() model.Cloud { return model.AWS }

// Scopes lists accounts: the organization's when organization is true,
// else the configured account IDs, else the caller's own account.
func (p *Provider) Scopes(ctx context.Context, patterns []string) ([]provider.Scope, error) {
	cfg := p.opts.Scopes
	var scopes []provider.Scope
	switch {
	case cfg.Organization:
		accts, err := p.b.Accounts(ctx)
		if err != nil {
			return nil, fmt.Errorf("AWS Organizations: %w", err)
		}
		p.mu.Lock()
		for _, a := range accts {
			p.accounts[a.ID] = a
			scopes = append(scopes, provider.Scope{Cloud: model.AWS, ID: a.ID, Name: a.Name})
		}
		p.mu.Unlock()
		scopes = provider.SelectScopes(scopes, cfg.Accounts)
	case len(cfg.Accounts) > 0:
		for _, id := range cfg.Accounts {
			if strings.ContainsAny(id, "*?[") {
				return nil, fmt.Errorf("scopes.aws.accounts: %q is a glob, which needs organization: true", id)
			}
			scopes = append(scopes, provider.Scope{Cloud: model.AWS, ID: id})
		}
	default:
		id, err := p.identity(ctx)
		if err != nil {
			return nil, err
		}
		scopes = []provider.Scope{{Cloud: model.AWS, ID: id.Account}}
	}
	if len(cfg.ExcludeAccounts) > 0 {
		excluded := provider.SelectScopes(scopes, cfg.ExcludeAccounts)
		scopes = slices.DeleteFunc(scopes, func(s provider.Scope) bool { return slices.Contains(excluded, s) })
	}
	scopes = provider.SelectScopes(scopes, patterns)
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].ID < scopes[j].ID })
	return slices.Compact(scopes), nil
}

func (p *Provider) identity(ctx context.Context) (identity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.caller != nil {
		return *p.caller, nil
	}
	id, err := p.b.Caller(ctx)
	if err != nil {
		return identity{}, fmt.Errorf("AWS credentials: %w", err)
	}
	p.caller = &id
	return id, nil
}

// management returns the Organizations management account ID, or "" when it
// cannot be read (the account is then never treated as the management one).
func (p *Provider) management(ctx context.Context) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mgmt == nil {
		id, err := p.b.ManagementAccount(ctx)
		if err != nil {
			p.opts.Log.Debug("management account unknown", "error", err)
		}
		p.mgmt = &id
	}
	return *p.mgmt
}

// Discover reads one account: AWS Config for bulk types, then each list
// adapter. A failure of one region or adapter is reported in
// Discovery.Errors; only a failure that affects everything (credentials, the
// aggregator) fails the scope.
func (p *Provider) Discover(ctx context.Context, s provider.Scope, types []*catalog.Entry) (provider.Discovery, error) {
	split := provider.SplitTypes(types, func(name string) bool { _, ok := listers[name]; return ok })
	d := provider.Discovery{Uncovered: split.Uncovered}
	id, err := p.identity(ctx)
	if err != nil {
		return d, err
	}
	if err := p.b.Prepare(ctx, s.ID); err != nil {
		return d, err
	}
	agg := p.opts.Scopes.ConfigAggregator
	var regions []string
	needRegions := len(split.Bulk) > 0 && agg == nil
	for name := range split.List {
		needRegions = needRegions || !listers[name].global
	}
	if needRegions {
		if regions, err = p.regions(ctx, s.ID); err != nil {
			return d, fmt.Errorf("listing regions: %w", err)
		}
	}

	var tasks []task
	if natives := provider.NativeTypes(split.Bulk); len(natives) > 0 {
		if agg != nil {
			// One aggregator failure means no bulk data for the account.
			rows, err := p.selectAll(ctx, buildQueries(natives, s.ID), func(q string) ([]string, error) {
				return p.b.SelectAggregate(ctx, *agg, q)
			})
			if err != nil {
				return d, fmt.Errorf("AWS Config aggregator %s: %w", agg.Name, err)
			}
			res, err := parseRows(rows)
			if err != nil {
				return d, err
			}
			d.Resources = append(d.Resources, res...)
		} else {
			for _, region := range regions {
				tasks = append(tasks, task{label: "AWS Config in " + region, run: func() ([]model.Resource, error) {
					rows, err := p.selectAll(ctx, buildQueries(natives, ""), func(q string) ([]string, error) {
						return p.b.Select(ctx, s.ID, region, q)
					})
					if err != nil {
						return nil, err
					}
					return parseRows(rows)
				}})
			}
		}
	}
	listNames := make([]string, 0, len(split.List))
	for name := range split.List {
		listNames = append(listNames, name)
	}
	sort.Strings(listNames)
	for _, name := range listNames {
		entries := split.List[name]
		req := p.listRequest(ctx, s, id, name, entries)
		label := displays(entries)
		if listers[name].global {
			tasks = append(tasks, task{label: label, run: func() ([]model.Resource, error) { return p.b.List(ctx, name, req) }})
			continue
		}
		for _, region := range regions {
			r := req
			r.Region = region
			tasks = append(tasks, task{label: label + " in " + region, run: func() ([]model.Resource, error) { return p.b.List(ctx, name, r) }})
		}
	}
	res, errs := runTasks(tasks, p.opts.RegionWorkers)
	d.Resources = dedupe(append(d.Resources, res...))
	d.Errors = errs
	return d, nil
}

func (p *Provider) listRequest(ctx context.Context, s provider.Scope, id identity, adapter string, entries []*catalog.Entry) listRequest {
	req := listRequest{Account: s.ID, Partition: id.Partition, NativeTypes: provider.NativeTypes(entries), AccountName: s.Name}
	if adapter == "organizations" {
		req.Management = p.management(ctx) == s.ID
		p.mu.Lock()
		req.AccountARN = p.accounts[s.ID].ARN
		p.mu.Unlock()
	}
	return req
}

func (p *Provider) regions(ctx context.Context, account string) ([]string, error) {
	if r := p.opts.Scopes.Regions; len(r) > 0 {
		return r, nil
	}
	return p.b.Regions(ctx, account)
}

// selectAll runs each query and concatenates the rows.
func (p *Provider) selectAll(_ context.Context, queries []string, run func(string) ([]string, error)) ([]string, error) {
	var rows []string
	for _, q := range queries {
		p.opts.Log.Debug("AWS Config query", "expression", q)
		r, err := run(q)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	return rows, nil
}

// task is one independently failing read within an account.
type task struct {
	label string
	run   func() ([]model.Resource, error)
}

// runTasks runs tasks with a bounded pool, keeping results in task order.
func runTasks(tasks []task, workers int) ([]model.Resource, []error) {
	type outcome struct {
		res []model.Resource
		err error
	}
	out := make([]outcome, len(tasks))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := t.run()
			out[i] = outcome{res, err}
		}()
	}
	wg.Wait()
	var res []model.Resource
	var errs []error
	for i, o := range out {
		if o.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", tasks[i].label, o.err))
			continue
		}
		res = append(res, o.res...)
	}
	return res, errs
}

// dedupe drops repeated IDs (global resources recorded in several regions)
// and sorts by ID.
func dedupe(rs []model.Resource) []model.Resource {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	return slices.CompactFunc(rs, func(a, b model.Resource) bool { return a.ID == b.ID })
}

func displays(entries []*catalog.Entry) string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Display
	}
	return strings.Join(names, ", ")
}

// ParentTags returns the account's tags from AWS Organizations.
func (p *Provider) ParentTags(ctx context.Context, r model.Resource) ([]model.Tag, error) {
	p.mu.Lock()
	t, ok := p.tags[r.Scope]
	p.mu.Unlock()
	if ok {
		return t, nil
	}
	t, err := p.b.AccountTags(ctx, r.Scope)
	if err != nil {
		return nil, fmt.Errorf("account %s tags: %w", r.Scope, err)
	}
	p.mu.Lock()
	p.tags[r.Scope] = t
	p.mu.Unlock()
	return t, nil
}
