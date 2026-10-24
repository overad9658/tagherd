// Package wiz validates the Wiz graph queries used by policy overrides.
// The GraphQL client that runs them arrives in phase 7.
package wiz

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// TagCondition is one key, or key/value pair, inside a tag operator.
type TagCondition struct {
	Path     string // JSON path of the operator, for messages
	Operator string // TAG_CONTAINS_ANY | TAG_DOES_NOT_CONTAIN_ANY
	Key      string
	Value    *string // nil when only the key is tested
}

// Query is a validated override query. Raw is sent to Wiz unchanged.
type Query struct {
	Raw  json.RawMessage
	Tags []TagCondition // every tag condition, in a stable (sorted-key) order
}

// Issue is one problem found while validating, located by JSON path.
type Issue struct {
	Path    string
	Message string
}

func (i Issue) Error() string { return i.Path + ": " + i.Message }

// The v1 subset. Anything else is rejected so a query tagherd cannot reason
// about never reaches Wiz.
var (
	queryKeys   = set("type", "select", "where", "relationships")
	withKeys    = set("type", "select", "where")
	relKeys     = set("type", "negate", "with")
	whereFields = set("nativeType", "cloudPlatform", "tags")
	fieldOps    = set("EQUALS")
	tagOps      = set("TAG_CONTAINS_ANY", "TAG_DOES_NOT_CONTAIN_ANY")
	relTypes    = set("USES")
	tagCondKeys = set("key", "value")
	relTypeKeys = set("type")
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// Parse validates raw against the supported subset. path prefixes every issue.
func Parse(raw json.RawMessage, path string) (*Query, []Issue) {
	v := &validator{}
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, []Issue{{path, "invalid JSON: " + err.Error()}}
	}
	v.query(root, path, queryKeys)
	if len(v.issues) > 0 {
		return nil, v.issues
	}
	return &Query{Raw: raw, Tags: v.tags}, nil
}

type validator struct {
	issues []Issue
	tags   []TagCondition
}

func (v *validator) fail(path, format string, args ...any) {
	v.issues = append(v.issues, Issue{path, fmt.Sprintf(format, args...)})
}

func (v *validator) object(x any, path string, allowed map[string]bool) map[string]any {
	m, ok := x.(map[string]any)
	if !ok {
		v.fail(path, "must be an object")
		return nil
	}
	for _, k := range sortedKeys(m) {
		if !allowed[k] {
			v.fail(path+"."+k, "unsupported key %q", k)
		}
	}
	return m
}

func (v *validator) query(x any, path string, allowed map[string]bool) {
	m := v.object(x, path, allowed)
	if m == nil {
		return
	}
	if t, ok := m["type"]; ok {
		v.stringList(t, path+".type")
	} else {
		v.fail(path, "type is required")
	}
	if s, ok := m["select"]; ok {
		if _, isBool := s.(bool); !isBool {
			v.fail(path+".select", "must be true or false")
		}
	}
	if w, ok := m["where"]; ok {
		v.where(w, path+".where")
	}
	if r, ok := m["relationships"]; ok {
		v.relationships(r, path+".relationships")
	}
}

func (v *validator) where(x any, path string) {
	m, ok := x.(map[string]any)
	if !ok {
		v.fail(path, "must be an object")
		return
	}
	for _, k := range sortedKeys(m) {
		p := path + "." + k
		switch {
		case k == "_or":
			list, ok := m[k].([]any)
			if !ok || len(list) == 0 {
				v.fail(p, "must be a non-empty list")
				continue
			}
			for i, w := range list {
				v.where(w, fmt.Sprintf("%s[%d]", p, i))
			}
		case k == "tags":
			v.tagOperator(m[k], p)
		case whereFields[k]:
			v.fieldOperator(m[k], p)
		default:
			v.fail(p, "unsupported field %q", k)
		}
	}
}

func (v *validator) fieldOperator(x any, path string) {
	m := v.object(x, path, fieldOps)
	if m == nil {
		return
	}
	if len(m) != 1 {
		v.fail(path, "needs exactly one operator")
	}
	for _, op := range sortedKeys(m) {
		if fieldOps[op] {
			v.stringList(m[op], path+"."+op)
		}
	}
}

func (v *validator) tagOperator(x any, path string) {
	m := v.object(x, path, tagOps)
	if m == nil {
		return
	}
	if len(m) != 1 {
		v.fail(path, "needs exactly one operator")
	}
	for _, op := range sortedKeys(m) {
		if !tagOps[op] {
			continue
		}
		p := path + "." + op
		list, ok := m[op].([]any)
		if !ok || len(list) == 0 {
			v.fail(p, "must be a non-empty list")
			continue
		}
		for i, c := range list {
			cp := fmt.Sprintf("%s[%d]", p, i)
			cm := v.object(c, cp, tagCondKeys)
			if cm == nil {
				continue
			}
			key, ok := cm["key"].(string)
			if !ok || key == "" {
				v.fail(cp, "key must be a non-empty string")
				continue
			}
			cond := TagCondition{Path: cp, Operator: op, Key: key}
			if raw, has := cm["value"]; has {
				s, ok := raw.(string)
				if !ok {
					v.fail(cp+".value", "must be a string")
					continue
				}
				cond.Value = &s
			}
			v.tags = append(v.tags, cond)
		}
	}
}

func (v *validator) relationships(x any, path string) {
	list, ok := x.([]any)
	if !ok || len(list) == 0 {
		v.fail(path, "must be a non-empty list")
		return
	}
	for i, r := range list {
		p := fmt.Sprintf("%s[%d]", path, i)
		m := v.object(r, p, relKeys)
		if m == nil {
			continue
		}
		if neg, _ := m["negate"].(bool); !neg {
			v.fail(p+".negate", "only negated relationships are supported")
		}
		types, ok := m["type"].([]any)
		if !ok || len(types) == 0 {
			v.fail(p+".type", "must be a non-empty list")
		}
		for j, t := range types {
			tp := fmt.Sprintf("%s.type[%d]", p, j)
			tm := v.object(t, tp, relTypeKeys)
			if tm == nil {
				continue
			}
			if name, _ := tm["type"].(string); !relTypes[name] {
				v.fail(tp+".type", "unsupported relationship %q (supported: USES)", name)
			}
		}
		if w, ok := m["with"]; ok {
			v.query(w, p+".with", withKeys)
		} else {
			v.fail(p, "with is required")
		}
	}
}

func (v *validator) stringList(x any, path string) {
	list, ok := x.([]any)
	if !ok || len(list) == 0 {
		v.fail(path, "must be a non-empty list of strings")
		return
	}
	for i, s := range list {
		if str, ok := s.(string); !ok || strings.TrimSpace(str) == "" {
			v.fail(fmt.Sprintf("%s[%d]", path, i), "must be a non-empty string")
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
