package gcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
)

// rmBinding is one Resource Manager tag on a resource, by short names.
type rmBinding struct {
	Key, Value string
	ValueName  string // tagValues/123
	Inherited  bool
}

// writeBackend is the GCP write surface: per-service label calls and
// Resource Manager tags.
type writeBackend interface {
	// Caller names the credential's account.
	Caller(ctx context.Context) (string, error)
	// Labels reads a resource's labels through its own service.
	Labels(ctx context.Context, t provider.Target) (map[string]string, error)
	// SetLabels replaces the labels with the given full set.
	SetLabels(ctx context.Context, t provider.Target, labels map[string]string) error
	// EffectiveTags lists the Resource Manager tags on a resource, its own
	// and inherited.
	EffectiveTags(ctx context.Context, t provider.Target) ([]rmBinding, error)
	// TagValue returns the tag value namespace/key/value (tagValues/123),
	// creating the value when the key exists but the value does not.
	TagValue(ctx context.Context, namespace, key, value string) (string, error)
	// Bind binds a tag value to a resource; Unbind removes one binding.
	Bind(ctx context.Context, t provider.Target, valueName string) error
	Unbind(ctx context.Context, t provider.Target, valueName string) error
}

// Writer returns a tag writer for a project. Writes are limited by the
// plan to the Compute, Database and Storage groups.
func (p *Provider) Writer(ctx context.Context, s provider.Scope) (provider.TagWriter, error) {
	if p.w == nil {
		return nil, errors.New("GCP writes are not configured")
	}
	actor, err := p.w.Caller(ctx)
	if err != nil {
		p.opts.Log.Warn("GCP caller identity unknown", "error", err)
		actor = "unknown"
	}
	ns := p.opts.Scopes.TagNamespace
	if ns == "" {
		ns = p.opts.Scopes.OrganizationID
	}
	if ns == "" {
		ns = s.ID
	}
	return &writer{b: p.w, actor: actor, namespace: ns}, nil
}

type writer struct {
	b         writeBackend
	actor     string
	namespace string
}

func (w *writer) Actor(context.Context) (string, error) { return w.actor, nil }

// Read returns labels and effective Resource Manager tags, as discovery
// reports them.
func (w *writer) Read(ctx context.Context, targets []provider.Target) (map[string][]model.Tag, map[string]error) {
	out, errs := map[string][]model.Tag{}, map[string]error{}
	for _, t := range targets {
		labelMap, err := w.b.Labels(ctx, t)
		if err != nil {
			errs[t.ID] = fmt.Errorf("reading labels: %w", err)
			continue
		}
		bindings, err := w.b.EffectiveTags(ctx, t)
		if err != nil {
			errs[t.ID] = fmt.Errorf("reading Resource Manager tags: %w", err)
			continue
		}
		tags := labels(labelMap)
		tags = append(tags, rmTagsOf(bindings)...)
		out[t.ID] = tags
	}
	return out, errs
}

// rmTagsOf keeps one value per key: the resource's own binding wins over
// an inherited one, as in discovery.
func rmTagsOf(bindings []rmBinding) []model.Tag {
	byKey := map[string]rmBinding{}
	for _, b := range bindings {
		if prev, ok := byKey[b.Key]; ok && !prev.Inherited {
			continue
		}
		byKey[b.Key] = b
	}
	out := make([]model.Tag, 0, len(byKey))
	for _, k := range sortedKeys(byKey) {
		out = append(out, model.Tag{Key: k, Value: byKey[k].Value, Kind: model.KindRMTag})
	}
	return out
}

// Write merges labels with one update per resource, then binds each
// Resource Manager tag, replacing the resource's own binding for the same
// key. Inherited tags are overridden, never removed.
func (w *writer) Write(ctx context.Context, writes []provider.TagWrite) map[string]error {
	errs := map[string]error{}
	for _, wr := range writes {
		if err := w.write(ctx, wr); err != nil {
			errs[wr.Target.ID] = err
		}
	}
	return errs
}

func (w *writer) write(ctx context.Context, wr provider.TagWrite) error {
	newLabels := map[string]string{}
	var rm []model.Tag
	for _, t := range wr.Tags {
		if t.Kind == model.KindRMTag {
			rm = append(rm, t)
		} else {
			newLabels[t.Key] = t.Value
		}
	}
	if len(newLabels) > 0 {
		current, err := w.b.Labels(ctx, wr.Target)
		if err != nil {
			return fmt.Errorf("reading labels: %w", err)
		}
		merged := maps.Clone(current)
		if merged == nil {
			merged = map[string]string{}
		}
		maps.Copy(merged, newLabels)
		if err := w.b.SetLabels(ctx, wr.Target, merged); err != nil {
			return fmt.Errorf("writing labels: %w", err)
		}
	}
	if len(rm) == 0 {
		return nil
	}
	bindings, err := w.b.EffectiveTags(ctx, wr.Target)
	if err != nil {
		return fmt.Errorf("reading Resource Manager tags: %w", err)
	}
	for _, t := range rm {
		value, err := w.b.TagValue(ctx, w.namespace, t.Key, t.Value)
		if err != nil {
			return fmt.Errorf("tag value %s/%s/%s: %w", w.namespace, t.Key, t.Value, err)
		}
		for _, b := range bindings {
			if b.Key == t.Key && !b.Inherited && b.ValueName != value {
				if err := w.b.Unbind(ctx, wr.Target, b.ValueName); err != nil {
					return fmt.Errorf("replacing the %s binding: %w", t.Key, err)
				}
			}
		}
		if err := w.b.Bind(ctx, wr.Target, value); err != nil {
			return fmt.Errorf("binding %s: %w", t.Key, err)
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
