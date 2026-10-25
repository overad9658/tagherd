package engine

import (
	"strings"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/policy"
)

// Tag names with rules beyond the dictionary.
const (
	tagDeploymentType = "DeploymentType"
	tagAdminEmail     = policy.AdminEmail
	tagOwningOrg      = "OwningOrg"
	defaultDeployment = "Manual" // a missing DeploymentType is treated as Manual
)

// result is the outcome of checking one required tag on one resource.
type result struct {
	code              model.Code // empty when compliant
	current, proposed string
	fixable           bool
}

// checkTag applies the spec's per-tag rules in order and returns the first
// that fails.
func checkTag(cloud model.Cloud, def *policy.TagDef, tags []model.Tag) result {
	var cands []model.Tag
	for _, t := range tags {
		if strings.EqualFold(t.Key, def.Name) {
			cands = append(cands, t)
		}
	}

	// 1. Keys that differ only by case and hold conflicting values. Azure
	// tag names ignore case, so it cannot hold two.
	if cloud != model.Azure && conflicting(cands) {
		parts := make([]string, len(cands))
		for i, c := range cands {
			parts[i] = c.Key + "=" + c.Value
		}
		return result{code: model.DuplicateKey, current: strings.Join(parts, ", ")}
	}

	// 2. Absent.
	if len(cands) == 0 {
		if def.Name == tagDeploymentType {
			return result{code: model.MissingTag, proposed: defaultDeployment, fixable: true}
		}
		return result{code: model.MissingTag}
	}
	t := preferred(def.Name, cands)
	value := strings.TrimSpace(t.Value)

	// 3. Present but empty.
	if value == "" {
		if def.Name == tagDeploymentType { // empty is treated like missing
			return result{code: model.EmptyValue, current: t.Value, proposed: defaultDeployment, fixable: true}
		}
		return result{code: model.EmptyValue, current: t.Value}
	}

	// 4-6. Value checks. DeploymentType is never corrected, only reported.
	canonical := t.Value
	if def.Enumerated() {
		c, ok := def.Canonical(value)
		switch {
		case !ok && def.Name == tagDeploymentType:
			return result{code: model.InvalidDeploymentType, current: t.Value}
		case !ok:
			return result{code: model.InvalidValue, current: t.Value}
		}
		canonical = c
	}
	if def.Name == tagAdminEmail && !policy.ValidEmail(value) {
		return result{code: model.InvalidValue, current: t.Value}
	}

	// 7. Spelling: right words, wrong case.
	keyOK := t.Key == def.Name ||
		cloud == model.Azure || // Azure tag names ignore case
		(t.Kind == model.KindLabel && t.Key == strings.ToLower(def.Name))
	valueOK := t.Value == canonical ||
		(t.Kind == model.KindLabel && t.Value == strings.ToLower(canonical))
	switch {
	case !keyOK:
		return result{
			code:     model.CaseMismatch,
			current:  t.Key + "=" + t.Value,
			proposed: def.Name + "=" + canonical,
			fixable:  true,
		}
	case !valueOK:
		return result{code: model.CaseMismatch, current: t.Value, proposed: canonical, fixable: true}
	}
	return result{}
}

// conflicting reports whether candidates hold more than one distinct value,
// ignoring case and surrounding space.
func conflicting(cands []model.Tag) bool {
	for _, c := range cands[min(1, len(cands)):] {
		if !strings.EqualFold(strings.TrimSpace(c.Value), strings.TrimSpace(cands[0].Value)) {
			return true
		}
	}
	return false
}

// preferred picks the tag to judge among same-valued candidates: the exact
// dictionary spelling first, then a GCP Resource Manager tag, then the first.
func preferred(name string, cands []model.Tag) model.Tag {
	for _, c := range cands {
		if c.Key == name {
			return c
		}
	}
	for _, c := range cands {
		if c.Kind == model.KindRMTag {
			return c
		}
	}
	return cands[0]
}
