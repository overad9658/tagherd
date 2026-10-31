// Package policy loads and validates tags.json and the policy group files.
package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/overad9658/tagherd/internal/catalog"
	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/wiz"
)

// DictionaryFile is the tag dictionary's file name inside the policy directory.
const DictionaryFile = "tags.json"

// Load reads every policy file in dir and validates it against cat. The set
// is nil when the report has errors; warnings and info never block loading.
func Load(dir string, cat *catalog.Catalog) (*Set, *Report) {
	return LoadFS(os.DirFS(dir), cat)
}

// LoadFS is Load over any file system, for tests.
func LoadFS(fsys fs.FS, cat *catalog.Catalog) (*Set, *Report) {
	r := &Report{}
	l := &loader{r: r, cat: cat, hash: sha256.New()}
	set := l.load(fsys)
	r.sort()
	if r.Count(LevelError) > 0 {
		return nil, r
	}
	return set, r
}

type loader struct {
	r    *Report
	cat  *catalog.Catalog
	dict *Dictionary
	hash hash.Hash
}

func (l *loader) load(fsys fs.FS) *Set {
	data, err := fs.ReadFile(fsys, DictionaryFile)
	if err != nil {
		l.r.add(LevelError, DictionaryFile, "", "cannot read: %v", unwrapPathErr(err))
		return nil
	}
	l.hashFile(DictionaryFile, data)
	l.dict = parseDictionary(data, l.r)
	if l.dict == nil {
		return nil
	}

	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		l.r.add(LevelError, ".", "", "cannot list policy files: %v", err)
		return nil
	}
	sort.Strings(names)
	set := &Set{Dict: l.dict, ByType: map[TypeKey][]*Group{}, Catalog: l.cat}
	byName := map[string]*Group{}
	for _, name := range names {
		if name == DictionaryFile {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			l.r.add(LevelError, name, "", "cannot read: %v", unwrapPathErr(err))
			continue
		}
		l.hashFile(name, data)
		g := l.group(name, data)
		if g == nil {
			continue
		}
		if prev, dup := byName[strings.ToLower(g.Name)]; dup {
			l.r.add(LevelError, name, "name", "group name %q is already used by %s", g.Name, prev.File)
			continue
		}
		byName[strings.ToLower(g.Name)] = g
		set.Groups = append(set.Groups, g)
	}
	if len(set.Groups) == 0 && l.r.Count(LevelError) == 0 {
		l.r.add(LevelError, ".", "", "no group files found next to %s", DictionaryFile)
	}
	sort.Slice(set.Groups, func(i, j int) bool { return set.Groups[i].Name < set.Groups[j].Name })
	l.index(set)
	set.Hash = hex.EncodeToString(l.hash.Sum(nil))
	return set
}

// hashFile feeds the file's canonical JSON into the policy hash, so whitespace
// and key order never change it. Unparseable files hash as raw bytes.
func (l *loader) hashFile(name string, data []byte) {
	var v any
	canon := data
	if json.Unmarshal(data, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			canon = b
		}
	}
	_, _ = fmt.Fprintf(l.hash, "%s\n%d\n", name, len(canon)) // hash writes never fail
	l.hash.Write(canon)
}

func (l *loader) group(file string, data []byte) *Group {
	var gf groupFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&gf); err != nil {
		l.r.add(LevelError, file, "", "%v", err)
		return nil
	}
	errsBefore := l.r.Count(LevelError)
	g := &Group{File: file, Targets: map[model.Cloud][]Target{}}

	if gf.Name == nil || strings.TrimSpace(*gf.Name) == "" {
		l.r.add(LevelError, file, "name", "name is required")
	} else {
		g.Name = strings.TrimSpace(*gf.Name)
	}
	if gf.Description == nil || strings.TrimSpace(*gf.Description) == "" {
		l.r.add(LevelInfo, file, "description", "description is empty")
	} else {
		g.Description = *gf.Description
	}

	l.tags(g, gf.Tags)
	l.targets(g, gf.Target)
	l.filters(g, gf.Filters)
	l.overrides(g, gf.Override)

	if l.r.Count(LevelError) > errsBefore {
		return nil
	}
	return g
}

func (l *loader) tags(g *Group, tags []string) {
	if len(tags) == 0 {
		l.r.add(LevelError, g.File, "tags", "at least one required tag is needed")
		return
	}
	seen := map[*TagDef]bool{}
	for i, name := range tags {
		p := fmt.Sprintf("tags[%d]", i)
		t, ok := l.dict.Lookup(name)
		switch {
		case !ok:
			l.r.add(LevelError, g.File, p, "tag %q is not in %s", name, DictionaryFile)
		case seen[t]:
			l.r.add(LevelWarning, g.File, p, "tag %q is listed twice", name)
		default:
			seen[t] = true
			g.Tags = append(g.Tags, t)
			g.TagNames = append(g.TagNames, t.Name)
		}
	}
}

func (l *loader) targets(g *Group, target map[string][]string) {
	if target == nil {
		l.r.add(LevelError, g.File, "target", "target is required")
		return
	}
	for key := range target {
		if !isCloudKey(key) {
			l.r.add(LevelError, g.File, "target."+key, "unknown cloud %q (want AWS, Azure, GCP or OCI)", key)
		}
	}
	for _, cloud := range model.Clouds {
		list, ok := target[string(cloud)]
		p := "target." + string(cloud)
		if !ok {
			l.r.add(LevelError, g.File, p, "missing; use an empty list when nothing is checked on %s", cloud)
			continue
		}
		seen := map[*catalog.Entry]string{}
		for i, display := range list {
			ip := fmt.Sprintf("%s[%d]", p, i)
			display = strings.TrimSpace(display)
			if display == "" {
				l.r.add(LevelError, g.File, ip, "display name must not be empty")
				continue
			}
			if !strings.HasPrefix(display, cloud.Prefix()) {
				l.r.add(LevelWarning, g.File, ip, "%q is listed under %s but named for another cloud; skipped until it is moved", display, cloud)
				continue
			}
			e, ok := l.cat.Lookup(cloud, display)
			if !ok {
				l.r.add(LevelError, g.File, ip, "%q is not in the type catalog", display)
				continue
			}
			canon := l.cat.Canonical(e)
			if prev, dup := seen[canon]; dup {
				if strings.EqualFold(prev, display) {
					l.r.add(LevelWarning, g.File, ip, "%q is listed twice", display)
				} else {
					l.r.add(LevelWarning, g.File, ip, "%q is the same type as %q; keep one", display, prev)
				}
				continue
			}
			if e.AliasOf != "" {
				l.r.add(LevelInfo, g.File, ip, "%q is treated as %q", display, e.AliasOf)
			}
			seen[canon] = display
			g.Targets[cloud] = append(g.Targets[cloud], Target{Display: display, Entry: canon})
		}
	}
}

func (l *loader) filters(g *Group, filters map[string]filterFile) {
	for _, key := range sortedKeys(filters) {
		f := filters[key]
		p := "filters." + key
		cloud, ok := cloudKey(key)
		if !ok {
			l.r.add(LevelError, g.File, p, "unknown cloud %q (want AWS, Azure, GCP or OCI)", key)
			continue
		}
		if f.Name == nil || len(f.Name.DoesNotContain) == 0 {
			l.r.add(LevelError, g.File, p, "needs name.DOES_NOT_CONTAIN with at least one string")
			continue
		}
		for i, s := range f.Name.DoesNotContain {
			if strings.TrimSpace(s) == "" {
				l.r.add(LevelError, g.File, fmt.Sprintf("%s.name.DOES_NOT_CONTAIN[%d]", p, i), "must not be empty")
			}
		}
		if len(g.Targets[cloud]) == 0 {
			l.r.add(LevelWarning, g.File, p, "filter has no effect: the group checks no %s types", cloud)
		}
		if g.Filters == nil {
			g.Filters = map[model.Cloud]NameFilter{}
		}
		g.Filters[cloud] = NameFilter{DoesNotContain: f.Name.DoesNotContain}
	}
}

func (l *loader) overrides(g *Group, overrides map[string]overrideFile) {
	for _, key := range sortedKeys(overrides) {
		o := overrides[key]
		p := "override." + key
		cloud, ok := cloudKey(key)
		if !ok {
			l.r.add(LevelError, g.File, p, "unknown cloud %q (want AWS, Azure, GCP or OCI)", key)
			continue
		}
		if !isNull(o.All) {
			l.r.add(LevelError, g.File, p+".all", "a replacement scope is not supported in v1; set it to null")
		}
		if isNull(o.Missing) {
			l.r.add(LevelError, g.File, p+".missing", "missing is required when an override is present")
			continue
		}
		q, issues := wiz.Parse(o.Missing, p+".missing")
		for _, is := range issues {
			l.r.add(LevelError, g.File, is.Path, "%s", is.Message)
		}
		if q == nil {
			continue
		}
		l.overrideDrift(g, q)
		if len(g.Targets[cloud]) == 0 {
			l.r.add(LevelWarning, g.File, p, "override has no effect: the group checks no %s types", cloud)
		}
		if g.Overrides == nil {
			g.Overrides = map[model.Cloud]*Override{}
		}
		g.Overrides[cloud] = &Override{Missing: q}
	}
}

// overrideDrift warns when the query's tag values disagree with the
// dictionary, because the query repeats them instead of referencing it.
func (l *loader) overrideDrift(g *Group, q *wiz.Query) {
	type listKey struct{ path, tag string }
	listed := map[listKey][]string{}
	var order []listKey
	for _, c := range q.Tags {
		t, ok := l.dict.Lookup(c.Key)
		if !ok {
			l.r.add(LevelWarning, g.File, c.Path, "tag %q is not in %s", c.Key, DictionaryFile)
			continue
		}
		if c.Value == nil || !t.Enumerated() {
			continue
		}
		if !slices.Contains(t.Allowed, *c.Value) {
			l.r.add(LevelWarning, g.File, c.Path, "value %q for %s is not an allowed value in %s", *c.Value, t.Name, DictionaryFile)
		}
		k := listKey{c.Path[:strings.LastIndex(c.Path, "[")], t.Name}
		if _, seen := listed[k]; !seen {
			order = append(order, k)
		}
		listed[k] = append(listed[k], *c.Value)
	}
	for _, k := range order {
		t, _ := l.dict.Lookup(k.tag)
		var missing []string
		for _, a := range t.Allowed {
			if !slices.Contains(listed[k], a) {
				missing = append(missing, a)
			}
		}
		if len(missing) > 0 {
			l.r.add(LevelWarning, g.File, k.path, "%s values do not match %s: missing %s", t.Name, DictionaryFile, strings.Join(missing, ", "))
		}
	}
}

// index builds ByType and warns about types that sit in more than one group.
func (l *loader) index(set *Set) {
	for _, g := range set.Groups {
		for _, cloud := range model.Clouds {
			for _, t := range g.Targets[cloud] {
				k := TypeKey{cloud, t.Entry.Display}
				for _, other := range set.ByType[k] {
					l.r.add(LevelWarning, g.File, "target."+string(cloud),
						"%q is also in group %q; the required tags are the union", t.Display, other.Name)
				}
				set.ByType[k] = append(set.ByType[k], g)
			}
		}
	}
}

func isCloudKey(k string) bool { _, ok := cloudKey(k); return ok }

// cloudKey accepts only the exact spelling used in policy files.
func cloudKey(k string) (model.Cloud, bool) {
	c := model.Cloud(k)
	return c, slices.Contains(model.Clouds, c)
}

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func unwrapPathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
