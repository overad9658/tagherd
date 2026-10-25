// Package scan runs discovery across scopes and hands the results to the
// engine. It owns concurrency and error isolation; the engine owns policy.
package scan

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/engine"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
	"github.com/overad9658/tagherd/internal/provider"
	"github.com/overad9658/tagherd/internal/wiz"
)

// OverrideRunner runs a policy override query (Wiz in production).
type OverrideRunner interface {
	Run(ctx context.Context, q *wiz.Query, scopes []provider.Scope) (map[string]bool, error)
}

// Options control one scan.
type Options struct {
	Clouds        []model.Cloud            // none selects all
	ScopePatterns map[model.Cloud][]string // --account, --subscription, --project
	Groups        []*policy.Group          // selected groups; nil selects all
	Types         []string                 // --type globs
	Concurrency   int                      // scopes discovered in parallel
	Overrides     OverrideRunner           // nil: overrides fall back
	Log           *slog.Logger
}

// Run discovers every selected scope and evaluates the result.
func Run(ctx context.Context, set *policy.Set, providers map[model.Cloud]provider.Provider, opts Options) engine.Result {
	in := Discover(ctx, set, providers, opts)
	return engine.Evaluate(set, in, engine.Options{Groups: opts.Groups, Types: opts.Types})
}

// Discover lists the selected scopes of every cloud, reads them through one
// pool of opts.Concurrency workers, and runs the selected groups' overrides.
// The result is in a fixed order whatever the concurrency.
func Discover(ctx context.Context, set *policy.Set, providers map[model.Cloud]provider.Provider, opts Options) engine.Input {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	groups := opts.Groups
	if groups == nil {
		groups = set.Groups
	}
	in := engine.Input{Overrides: map[engine.OverrideKey]*engine.OverrideResult{}}
	var jobs []job
	scopesByCloud := map[model.Cloud][]provider.Scope{}
	for _, cloud := range model.Clouds {
		if len(opts.Clouds) > 0 && !slices.Contains(opts.Clouds, cloud) {
			continue
		}
		types := typesFor(groups, cloud, opts.Types)
		if len(types) == 0 {
			continue
		}
		p := providers[cloud]
		if p == nil {
			opts.Log.Debug("no provider for cloud; skipped", "cloud", cloud)
			continue
		}
		scopes, err := p.Scopes(ctx, opts.ScopePatterns[cloud])
		if err != nil {
			in.Unreadable = append(in.Unreadable, model.UnreadableScope{Cloud: cloud, Scope: "*", Error: "listing scopes: " + err.Error()})
			continue
		}
		// A type with no adapter in the catalog is a gap on the whole cloud,
		// reported once rather than once per scope.
		types = slices.DeleteFunc(types, func(e *catalog.Entry) bool {
			if mode, _ := e.DiscoveryMode(); mode == catalog.DiscoverNone {
				in.Unmapped = append(in.Unmapped, model.CoverageGap{Cloud: cloud, Scope: "*", Type: e.Display})
				return true
			}
			return false
		})
		opts.Log.Info("discovering", "cloud", cloud, "scopes", len(scopes), "types", len(types))
		scopesByCloud[cloud] = scopes
		for _, s := range scopes {
			jobs = append(jobs, job{p: p, scope: s, types: types})
		}
	}
	discover(ctx, jobs, max(opts.Concurrency, 1), &in, opts.Log)
	for _, cloud := range model.Clouds {
		if scopes, ok := scopesByCloud[cloud]; ok {
			runOverrides(ctx, groups, cloud, scopes, opts, &in)
		}
	}
	return in
}

type job struct {
	p     provider.Provider
	scope provider.Scope
	types []*catalog.Entry
}

// typesFor returns the canonical catalog entries the selected groups name
// on a cloud, filtered by --type.
func typesFor(groups []*policy.Group, cloud model.Cloud, patterns []string) []*catalog.Entry {
	seen := map[*catalog.Entry]bool{}
	var out []*catalog.Entry
	for _, g := range groups {
		for _, t := range g.Targets[cloud] {
			if seen[t.Entry] || !engine.MatchAny(patterns, t.Entry.Display) {
				continue
			}
			seen[t.Entry] = true
			out = append(out, t.Entry)
		}
	}
	return out
}

// discover reads each scope in parallel. Results are kept in job order so
// the engine always sees the same input.
func discover(ctx context.Context, jobs []job, workers int, in *engine.Input, log *slog.Logger) {
	type outcome struct {
		d   provider.Discovery
		err error
	}
	results := make([]outcome, len(jobs))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			d, err := j.p.Discover(ctx, j.scope, j.types)
			log.Debug("scope read", "cloud", j.scope.Cloud, "scope", j.scope.ID, "resources", len(d.Resources), "took", time.Since(start).Round(time.Millisecond))
			results[i] = outcome{d, err}
		}()
	}
	wg.Wait()
	for i, j := range jobs {
		s, r := j.scope, results[i]
		if r.err != nil {
			log.Warn("scope could not be read", "cloud", s.Cloud, "scope", s.ID, "error", r.err)
			in.Unreadable = append(in.Unreadable, model.UnreadableScope{Cloud: s.Cloud, Scope: s.ID, Error: r.err.Error()})
			continue
		}
		for _, err := range r.d.Errors {
			log.Warn("part of a scope could not be read", "cloud", s.Cloud, "scope", s.ID, "error", err)
			in.Unreadable = append(in.Unreadable, model.UnreadableScope{Cloud: s.Cloud, Scope: s.ID, Error: err.Error()})
		}
		in.Resources = append(in.Resources, r.d.Resources...)
		for _, e := range r.d.Uncovered {
			in.Unmapped = append(in.Unmapped, model.CoverageGap{Cloud: s.Cloud, Scope: s.ID, Type: e.Display})
		}
	}
}

// runOverrides runs each selected group's override for the cloud once. A
// failure leaves the result out, so the engine falls back to the default test.
func runOverrides(ctx context.Context, groups []*policy.Group, cloud model.Cloud, scopes []provider.Scope, opts Options, in *engine.Input) {
	for _, g := range groups {
		o := g.Overrides[cloud]
		if o == nil {
			continue
		}
		if opts.Overrides == nil {
			opts.Log.Warn("no Wiz connection; override falls back to the default tag test", "group", g.Name, "cloud", cloud)
			continue
		}
		missing, err := opts.Overrides.Run(ctx, o.Missing, scopes)
		if err != nil {
			opts.Log.Warn("override query failed; falling back to the default tag test", "group", g.Name, "cloud", cloud, "error", err)
			continue
		}
		in.Overrides[engine.OverrideKey{Group: g.Name, Cloud: cloud}] = &engine.OverrideResult{Missing: missing}
	}
}
