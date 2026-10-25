// Package apply executes a plan's direct changes through each cloud's tag
// writer. It checks every change against the tag found just before the
// write, enforces the cloud's tag limits, never removes a tag, and records
// every attempt in the audit log. One failure never stops the others.
package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

// MaxAge is how old a plan may be without --allow-stale.
const MaxAge = 24 * time.Hour

// Gate errors, returned before anything is written.
var (
	ErrPolicyChanged = errors.New("the policy changed since the plan was made")
	ErrPlanTooOld    = errors.New("the plan is too old")
)

// Check runs the gates that come before any write: the plan's policy hash
// must match the current policy, and the plan must be under MaxAge old
// unless allowStale is set. allowStale never overrides a hash mismatch.
func Check(p model.Plan, policyHash string, now time.Time, allowStale bool) error {
	if p.PolicyHash != policyHash {
		return fmt.Errorf("%w (plan %s, now %s); run tagherd plan again", ErrPolicyChanged, short(p.PolicyHash), short(policyHash))
	}
	if age := now.Sub(p.Created); age > MaxAge && !allowStale {
		return fmt.Errorf("%w: made %s ago, more than %s; run tagherd plan again or pass --allow-stale",
			ErrPlanTooOld, age.Round(time.Minute), MaxAge)
	}
	return nil
}

func short(h string) string { return h[:min(len(h), 12)] }

// Options control Run.
type Options struct {
	Providers   map[model.Cloud]provider.Provider
	Concurrency int       // scopes written in parallel
	Audit       io.Writer // receives one JSON line per attempted change
	Now         func() time.Time
	Log         *slog.Logger
}

// Run applies the plan's direct changes. Terraform changes are reported as
// pending, and unwritable or unroutable ones as skipped. Results keep the
// plan's order.
func Run(ctx context.Context, p model.Plan, opts Options) model.ApplyReport {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Audit == nil {
		opts.Audit = io.Discard
	}
	r := &run{opts: opts, plan: p, results: make([]model.ChangeResult, len(p.Changes))}

	byScope := map[scopeKey][]int{}
	var order []scopeKey
	for i, c := range p.Changes {
		r.results[i] = model.ChangeResult{Change: c}
		switch c.Route {
		case model.RouteDirect:
			k := scopeKey{c.Cloud, c.Scope}
			if byScope[k] == nil {
				order = append(order, k)
			}
			byScope[k] = append(byScope[k], i)
		case model.RouteTerraform:
			r.set(i, model.OutcomePending, "the Terraform route arrives in phase 6")
		case model.RouteUnwritable:
			r.set(i, model.OutcomeSkipped, "this type is scanned but not written")
		default:
			r.set(i, model.OutcomeSkipped, "no write route")
		}
	}

	sem := make(chan struct{}, max(opts.Concurrency, 1))
	var wg sync.WaitGroup
	for _, k := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r.scope(ctx, k, byScope[k])
		}()
	}
	wg.Wait()

	rep := model.ApplyReport{PlanID: p.ID, Results: r.results}
	for _, res := range r.results {
		rep.Summary.Add(res.Outcome)
	}
	return rep
}

type scopeKey struct {
	cloud model.Cloud
	scope string
}

type run struct {
	opts    Options
	plan    model.Plan
	mu      sync.Mutex // guards results' outcomes and the audit writer
	results []model.ChangeResult
}

func (r *run) set(i int, o model.Outcome, detail string) {
	r.results[i].Outcome, r.results[i].Detail = o, detail
}

// record sets an attempted change's outcome and appends its audit line.
func (r *run) record(i int, actor, old string, o model.Outcome, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.set(i, o, detail)
	c := r.results[i].Change
	line, err := json.Marshal(model.AuditRecord{
		Time: r.opts.Now().UTC(), PlanID: r.plan.ID, Actor: actor,
		Cloud: c.Cloud, Scope: c.Scope, ResourceID: c.ResourceID, Type: c.Type,
		Tag: c.Tag, Kind: c.Kind, Old: old, New: c.New, Rule: c.Rule, Outcome: o, Detail: detail,
	})
	if err == nil {
		_, err = r.opts.Audit.Write(append(line, '\n'))
	}
	if err != nil {
		r.opts.Log.Error("audit log write failed", "resource", c.ResourceID, "tag", c.Tag, "error", err)
	}
}

// scope applies one scope's changes: one read and one write call, which
// the adapter batches.
func (r *run) scope(ctx context.Context, k scopeKey, idx []int) {
	fail := func(actor string, err error) {
		for _, i := range idx {
			r.record(i, actor, "", model.OutcomeFailed, err.Error())
		}
	}
	p := r.opts.Providers[k.cloud]
	if p == nil {
		fail("", fmt.Errorf("no %s provider configured", k.cloud))
		return
	}
	w, err := p.Writer(ctx, provider.Scope{Cloud: k.cloud, ID: k.scope})
	if err != nil {
		r.opts.Log.Warn("scope could not be written", "cloud", k.cloud, "scope", k.scope, "error", err)
		fail("", fmt.Errorf("write credentials: %w", err))
		return
	}
	actor, err := w.Actor(ctx)
	if err != nil {
		actor = "unknown"
		r.opts.Log.Warn("caller identity unknown; audit lines say unknown", "cloud", k.cloud, "scope", k.scope, "error", err)
	}

	// Group by resource, keeping plan order.
	byResource := map[string][]int{}
	var targets []provider.Target
	for _, i := range idx {
		c := r.results[i].Change
		if byResource[c.ResourceID] == nil {
			targets = append(targets, provider.Target{ID: c.ResourceID, NativeType: c.NativeType, Region: c.Region})
		}
		byResource[c.ResourceID] = append(byResource[c.ResourceID], i)
	}
	current, readErrs := w.Read(ctx, targets)

	type pending struct {
		target provider.Target
		idx    []int
		old    []string
		tags   []model.Tag
	}
	var writes []pending
	for _, t := range targets {
		ids := byResource[t.ID]
		if err := readErrs[t.ID]; err != nil {
			for _, i := range ids {
				r.record(i, actor, "", model.OutcomeFailed, "reading tags: "+err.Error())
			}
			continue
		}
		tags, ok := current[t.ID]
		if !ok {
			for _, i := range ids {
				r.record(i, actor, "", model.OutcomeFailed, "resource not found")
			}
			continue
		}
		pw := pending{target: t}
		for _, i := range ids {
			c := r.results[i].Change
			found, exists := lookup(c, tags)
			switch {
			case exists && sameValue(c, found.Value, c.New):
				r.record(i, actor, found.Value, model.OutcomeNoop, "")
			case c.Action == model.ActionAdd && exists:
				r.record(i, actor, found.Value, model.OutcomeStale, fmt.Sprintf("tag now exists with %q", found.Value))
			case c.Action == model.ActionUpdate && !exists:
				r.record(i, actor, "", model.OutcomeStale, "tag is gone")
			case c.Action == model.ActionUpdate && found.Value != c.Expect:
				r.record(i, actor, found.Value, model.OutcomeStale, fmt.Sprintf("found %q, plan expected %q", found.Value, c.Expect))
			default:
				pw.idx = append(pw.idx, i)
				pw.old = append(pw.old, found.Value)
				pw.tags = append(pw.tags, writeTag(c))
			}
		}
		if len(pw.idx) == 0 {
			continue
		}
		if err := checkLimits(k.cloud, tags, pw.tags); err != nil {
			for j, i := range pw.idx {
				r.record(i, actor, pw.old[j], model.OutcomeFailed, err.Error())
			}
			continue
		}
		writes = append(writes, pw)
	}
	if len(writes) == 0 {
		return
	}

	batch := make([]provider.TagWrite, len(writes))
	for j, pw := range writes {
		batch[j] = provider.TagWrite{Target: pw.target, Tags: pw.tags}
	}
	errs := w.Write(ctx, batch)
	for _, pw := range writes {
		err := errs[pw.target.ID]
		for j, i := range pw.idx {
			if err != nil {
				r.record(i, actor, pw.old[j], model.OutcomeFailed, err.Error())
			} else {
				r.record(i, actor, pw.old[j], model.OutcomeWritten, "")
			}
		}
	}
}

// lookup finds the tag a change writes, under the cloud's key rules.
func lookup(c model.Change, tags []model.Tag) (model.Tag, bool) {
	for _, t := range tags {
		var match bool
		switch {
		case c.Cloud == model.Azure: // tag names ignore case
			match = strings.EqualFold(t.Key, c.Tag)
		case c.Cloud == model.GCP && c.Kind == model.KindLabel:
			match = t.Kind == model.KindLabel && t.Key == strings.ToLower(c.Tag)
		case c.Cloud == model.GCP:
			match = t.Kind == model.KindRMTag && t.Key == c.Tag
		default:
			match = t.Key == c.Tag
		}
		if match {
			return t, true
		}
	}
	return model.Tag{}, false
}

// sameValue reports whether a found value already satisfies the change.
func sameValue(c model.Change, found, want string) bool {
	if c.Kind == model.KindLabel {
		return found == strings.ToLower(want)
	}
	return found == want
}

// writeTag is the tag sent to the writer: GCP labels are lowercase.
func writeTag(c model.Change) model.Tag {
	switch {
	case c.Cloud != model.GCP:
		return model.Tag{Key: c.Tag, Value: c.New, Kind: model.KindTag}
	case c.Kind == model.KindLabel:
		return model.Tag{Key: strings.ToLower(c.Tag), Value: strings.ToLower(c.New), Kind: model.KindLabel}
	}
	return model.Tag{Key: c.Tag, Value: c.New, Kind: model.KindRMTag}
}
