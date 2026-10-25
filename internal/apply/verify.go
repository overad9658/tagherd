package apply

import (
	"context"
	"fmt"

	"github.com/overad9658/tagherd/internal/engine"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
	"github.com/overad9658/tagherd/internal/provider"
)

// Verify re-reads the tags of every resource with a written change and
// evaluates it again, returning the findings still open (NOT_TAGGABLE
// aside). Tags are read through the writer, not bulk discovery, which can
// lag a write by minutes. A resource that cannot be re-read is a warning.
func Verify(ctx context.Context, set *policy.Set, rep model.ApplyReport, opts Options) ([]model.Finding, []string) {
	type key struct {
		cloud model.Cloud
		scope string
	}
	resources := map[key]map[string]model.Resource{}
	var order []key
	for _, res := range rep.Results {
		if res.Outcome != model.OutcomeWritten {
			continue
		}
		k := key{res.Cloud, res.Scope}
		if resources[k] == nil {
			resources[k] = map[string]model.Resource{}
			order = append(order, k)
		}
		resources[k][res.ResourceID] = model.Resource{
			Cloud: res.Cloud, Scope: res.Scope, Region: res.Region, Type: res.Type,
			NativeType: res.NativeType, ID: res.ResourceID, Name: res.Name,
		}
	}

	var in engine.Input
	var warnings []string
	for _, k := range order {
		p := opts.Providers[k.cloud]
		if p == nil {
			continue
		}
		w, err := p.Writer(ctx, provider.Scope{Cloud: k.cloud, ID: k.scope})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("verify: %s %s: %v", k.cloud, k.scope, err))
			continue
		}
		var targets []provider.Target
		for _, r := range resources[k] {
			targets = append(targets, provider.Target{ID: r.ID, NativeType: r.NativeType, Region: r.Region})
		}
		tags, errs := w.Read(ctx, targets)
		for id, err := range errs {
			warnings = append(warnings, fmt.Sprintf("verify: %s: %v", id, err))
		}
		for id, r := range resources[k] {
			if t, ok := tags[id]; ok {
				r.Tags = t
				in.Resources = append(in.Resources, r)
			}
		}
	}
	res := engine.Evaluate(set, in, engine.Options{})
	var out []model.Finding
	for _, f := range res.Findings {
		if f.Code != model.NotTaggable {
			out = append(out, f)
		}
	}
	return out, warnings
}
