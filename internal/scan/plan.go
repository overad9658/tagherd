package scan

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/overad9658/tagherd/internal/engine"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
	"github.com/overad9658/tagherd/internal/provider"
	"github.com/overad9658/tagherd/internal/ulid"
)

// PlanOptions extend a scan for tagherd plan.
type PlanOptions struct {
	Sources engine.Sources // Parents is filled in by Plan
	Now     time.Time
	Grace   time.Duration
}

// Plan scans, reads the parent tags the findings may inherit, and builds
// the plan. A parent whose tags cannot be read supplies nothing and is
// listed in the plan's warnings.
func Plan(ctx context.Context, set *policy.Set, providers map[model.Cloud]provider.Provider, opts Options, popts PlanOptions) (model.Plan, engine.Result) {
	in := Discover(ctx, set, providers, opts)
	res := engine.Evaluate(set, in, engine.Options{Groups: opts.Groups, Types: opts.Types})
	src := popts.Sources
	var warnings []string
	src.Parents, warnings = parentTags(ctx, providers, engine.ParentRequests(in, res), max(opts.Concurrency, 1))
	for _, w := range warnings {
		if opts.Log != nil {
			opts.Log.Warn(w)
		}
	}
	p := engine.BuildPlan(set, in, res, src, engine.PlanOptions{Now: popts.Now, Grace: popts.Grace})
	p.ID = ulid.New(popts.Now)
	p.Created = popts.Now.UTC()
	p.Summary.Warnings = append(slices.Clone(res.Summary.Warnings), warnings...)
	slices.Sort(p.Summary.Warnings)
	return p, res
}

// parentTags reads each parent once, workers at a time.
func parentTags(ctx context.Context, providers map[model.Cloud]provider.Provider, reqs map[engine.ParentKey]model.Resource, workers int) (map[engine.ParentKey][]model.Tag, []string) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		out      = map[engine.ParentKey][]model.Tag{}
		warnings []string
		sem      = make(chan struct{}, workers)
	)
	for k, r := range reqs {
		p := providers[k.Cloud]
		if p == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tags, err := p.ParentTags(ctx, r)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("parent tags of %s %s %s could not be read, so none are inherited: %v", k.Cloud, k.Scope, k.Parent, err))
				return
			}
			out[k] = tags
		}()
	}
	wg.Wait()
	return out, warnings
}
