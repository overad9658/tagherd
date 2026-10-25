// Package fake is an in-memory provider loaded from a YAML fixture. It stands
// in for the cloud adapters in tests and in `tagherd scan --fixture`.
package fake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/provider"
	"github.com/overad9658/tagherd/internal/wiz"
)

// Fixture is the YAML file layout.
//
//	scopes:
//	  - cloud: AWS
//	    id: "111122223333"
//	    name: prod
//	    fail: "AccessDenied"          # optional: Discover fails with this error
//	    tags: {Brand: Acme}            # parent tags for the scope
//	    parents:                       # parent tags by resource parent, instead
//	      rg-app: {Brand: Contoso}
//	    parent_fail: "AccessDenied"    # optional: ParentTags fails with this error
//	    writer_fail: "AccessDenied"    # optional: Writer fails with this error
//	    uncovered: ["AWS Comprehend Flywheel"]
//	    resources:
//	      - native_type: AWS::RDS::DBInstance
//	        id: arn:aws:rds:us-east-1:111122223333:db:orders
//	        name: orders
//	        region: us-east-1
//	        created: 2026-01-02T03:04:05Z
//	        attrs: {engine: postgres, cluster_member: "false"}
//	        tags: {Environment: production}
//	        labels: {}                 # GCP labels
//	        rm_tags: {}                # GCP Resource Manager tags
//	        write_fail: "Throttling"   # optional: writes to it fail
type Fixture struct {
	Scopes []FixtureScope `yaml:"scopes"`
	Wiz    *FixtureWiz    `yaml:"wiz"`
}

// FixtureWiz drives the fake override runner. Without it, overrides fall
// back to the default tag test.
//
//	wiz:
//	  missing: [arn:aws:ec2:us-east-1:111122223333:image/ami-1]
//	  fail: "timeout"                 # optional: every query fails
type FixtureWiz struct {
	Missing []string `yaml:"missing"`
	Fail    string   `yaml:"fail"`
}

type FixtureScope struct {
	Cloud      model.Cloud                  `yaml:"cloud"`
	ID         string                       `yaml:"id"`
	Name       string                       `yaml:"name"`
	Fail       string                       `yaml:"fail"`
	Tags       map[string]string            `yaml:"tags"`
	Parents    map[string]map[string]string `yaml:"parents"`
	ParentFail string                       `yaml:"parent_fail"`
	WriterFail string                       `yaml:"writer_fail"`
	Uncovered  []string                     `yaml:"uncovered"`
	Resources  []FixtureResource            `yaml:"resources"`
}

type FixtureResource struct {
	NativeType string            `yaml:"native_type"`
	ID         string            `yaml:"id"`
	Name       string            `yaml:"name"`
	Region     string            `yaml:"region"`
	Parent     string            `yaml:"parent"`
	Created    *time.Time        `yaml:"created"`
	Attrs      map[string]string `yaml:"attrs"`
	Tags       map[string]string `yaml:"tags"`
	Labels     map[string]string `yaml:"labels"`
	RMTags     map[string]string `yaml:"rm_tags"`
	WriteFail  string            `yaml:"write_fail"`
}

// Env is a loaded fixture: one provider per cloud it mentions, and the Wiz
// runner when the fixture configures one.
type Env struct {
	Providers map[model.Cloud]*Provider
	Wiz       *Wiz
}

// Load reads a fixture file.
func Load(file string) (*Env, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes fixture YAML.
func Parse(data []byte) (*Env, error) {
	var f Fixture
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("fixture: %w", err)
	}
	out := map[model.Cloud]*Provider{}
	for i, s := range f.Scopes {
		if !slices.Contains(model.Clouds, s.Cloud) {
			return nil, fmt.Errorf("fixture: scopes[%d]: cloud must be AWS, Azure or GCP", i)
		}
		p := out[s.Cloud]
		if p == nil {
			p = &Provider{cloud: s.Cloud}
			out[s.Cloud] = p
		}
		p.scopes = append(p.scopes, s)
	}
	env := &Env{Providers: out}
	if f.Wiz != nil {
		env.Wiz = &Wiz{missing: f.Wiz.Missing, fail: f.Wiz.Fail}
	}
	return env, nil
}

// Wiz is a fake override runner.
type Wiz struct {
	missing []string
	fail    string
}

// Run returns the fixture's missing IDs, whatever the query.
func (w *Wiz) Run(context.Context, *wiz.Query, []provider.Scope) (map[string]bool, error) {
	if w.fail != "" {
		return nil, errors.New(w.fail)
	}
	out := make(map[string]bool, len(w.missing))
	for _, id := range w.missing {
		out[id] = true
	}
	return out, nil
}

// Provider implements provider.Provider over a fixture.
type Provider struct {
	cloud  model.Cloud
	scopes []FixtureScope

	mu     sync.Mutex
	writes []Write
}

// Write is one recorded TagWriter.Write call.
type Write struct {
	Scope string
	ID    string
	Tags  []model.Tag
}

var _ provider.Provider = (*Provider)(nil)

func (p *Provider) Cloud() model.Cloud { return p.cloud }

func (p *Provider) Scopes(_ context.Context, patterns []string) ([]provider.Scope, error) {
	var out []provider.Scope
	for _, s := range p.scopes {
		out = append(out, provider.Scope{Cloud: p.cloud, ID: s.ID, Name: s.Name})
	}
	return provider.SelectScopes(out, patterns), nil
}

func (p *Provider) Discover(_ context.Context, sc provider.Scope, types []*catalog.Entry) (provider.Discovery, error) {
	s, err := p.scope(sc.ID)
	if err != nil {
		return provider.Discovery{}, err
	}
	if s.Fail != "" {
		return provider.Discovery{}, errors.New(s.Fail)
	}
	var d provider.Discovery
	for _, t := range types {
		for _, u := range s.Uncovered {
			if strings.EqualFold(u, t.Display) {
				d.Uncovered = append(d.Uncovered, t)
			}
		}
	}
	// Like a real adapter, return only the requested types.
	want := map[string]bool{}
	for _, t := range types {
		for _, nt := range t.NativeTypes {
			want[strings.ToLower(nt)] = true
		}
	}
	for _, r := range s.Resources {
		if want[strings.ToLower(r.NativeType)] {
			d.Resources = append(d.Resources, p.resource(s, r))
		}
	}
	return d, nil
}

func (p *Provider) resource(s *FixtureScope, r FixtureResource) model.Resource {
	res := model.Resource{
		Cloud: p.cloud, Scope: s.ID, Region: r.Region, NativeType: r.NativeType,
		ID: r.ID, Name: r.Name, Created: r.Created, Parent: r.Parent, Attrs: r.Attrs,
	}
	res.Tags = append(res.Tags, tags(r.Tags, model.KindTag)...)
	res.Tags = append(res.Tags, tags(r.Labels, model.KindLabel)...)
	res.Tags = append(res.Tags, tags(r.RMTags, model.KindRMTag)...)
	if res.Tags == nil {
		res.Tags = []model.Tag{}
	}
	return res
}

func (p *Provider) ParentTags(_ context.Context, r model.Resource) ([]model.Tag, error) {
	s, err := p.scope(r.Scope)
	if err != nil {
		return nil, err
	}
	if s.ParentFail != "" {
		return nil, errors.New(s.ParentFail)
	}
	kind := model.KindTag
	if p.cloud == model.GCP {
		kind = model.KindLabel // GCP parents are projects, with labels
	}
	if t, ok := s.Parents[r.Parent]; ok {
		return tags(t, kind), nil
	}
	return tags(s.Tags, kind), nil
}

func (p *Provider) Writer(_ context.Context, sc provider.Scope) (provider.TagWriter, error) {
	s, err := p.scope(sc.ID)
	if err != nil {
		return nil, err
	}
	if s.WriterFail != "" {
		return nil, errors.New(s.WriterFail)
	}
	return &writer{p: p, scope: sc.ID}, nil
}

// Writes returns every recorded write, in call order.
func (p *Provider) Writes() []Write {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.writes)
}

func (p *Provider) scope(id string) (*FixtureScope, error) {
	for i := range p.scopes {
		if p.scopes[i].ID == id {
			return &p.scopes[i], nil
		}
	}
	return nil, fmt.Errorf("fake %s: unknown scope %q", p.cloud, id)
}

type writer struct {
	p     *Provider
	scope string
}

func (w *writer) Actor(context.Context) (string, error) {
	return "fake:" + strings.ToLower(string(w.p.cloud)), nil
}

func (w *writer) Read(_ context.Context, targets []provider.Target) (map[string][]model.Tag, map[string]error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	out, errs := map[string][]model.Tag{}, map[string]error{}
	for _, t := range targets {
		r, err := w.find(t.ID)
		if err != nil {
			errs[t.ID] = err
			continue
		}
		res := w.p.resource(&FixtureScope{ID: w.scope}, *r)
		out[t.ID] = res.Tags
	}
	return out, errs
}

// Write records the call and merges the tags into the fixture, so a second
// read or scan sees them.
func (w *writer) Write(_ context.Context, writes []provider.TagWrite) map[string]error {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	errs := map[string]error{}
	for _, wr := range writes {
		r, err := w.find(wr.Target.ID)
		if err == nil && r.WriteFail != "" {
			err = errors.New(r.WriteFail)
		}
		if err != nil {
			errs[wr.Target.ID] = err
			continue
		}
		for _, t := range wr.Tags {
			m := &r.Tags
			switch t.Kind {
			case model.KindLabel:
				m = &r.Labels
			case model.KindRMTag:
				m = &r.RMTags
			}
			if *m == nil {
				*m = map[string]string{}
			}
			(*m)[t.Key] = t.Value
		}
		w.p.writes = append(w.p.writes, Write{Scope: w.scope, ID: wr.Target.ID, Tags: slices.Clone(wr.Tags)})
	}
	return errs
}

func (w *writer) find(id string) (*FixtureResource, error) {
	s, err := w.p.scope(w.scope)
	if err != nil {
		return nil, err
	}
	for i := range s.Resources {
		if s.Resources[i].ID == id {
			return &s.Resources[i], nil
		}
	}
	return nil, fmt.Errorf("fake: no resource %q in scope %s", id, w.scope)
}

// tags converts a fixture map to sorted tags, so output is deterministic.
func tags(m map[string]string, kind model.TagKind) []model.Tag {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]model.Tag, 0, len(keys))
	for _, k := range keys {
		out = append(out, model.Tag{Key: k, Value: m[k], Kind: kind})
	}
	return out
}
