package model

import (
	"cmp"
	"math"
	"slices"
)

// Summary is the scan's totals. Every list in it is sorted.
type Summary struct {
	Resources  Counts            `json:"resources"`
	Compliance ComplianceTables  `json:"compliance"`
	ByTag      map[string]int    `json:"findings_by_tag"`
	ByCode     map[Code]int      `json:"findings_by_code"`
	Unreadable []UnreadableScope `json:"unreadable_scopes"`
	Unmapped   []CoverageGap     `json:"unmapped_types"`
	// OverrideFallback lists "group (cloud)" for overrides that could not
	// run, so the default tag test was used instead.
	OverrideFallback []string `json:"override_fallback,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

// Counts follow one resource from discovery to a compliance result.
type Counts struct {
	Discovered  int `json:"discovered"`
	OutOfScope  int `json:"out_of_scope"` // type in no selected group
	Filtered    int `json:"filtered"`     // excluded by a group's name filter
	NotTaggable int `json:"not_taggable"`
	Evaluated   int `json:"evaluated"`
	Compliant   int `json:"compliant"`
}

// ComplianceTables report compliant over evaluated resources three ways.
type ComplianceTables struct {
	Overall Rate   `json:"overall"`
	ByGroup []Rate `json:"by_group"`
	ByCloud []Rate `json:"by_cloud"`
	ByScope []Rate `json:"by_scope"`
}

// Rate is compliant over evaluated for one key.
type Rate struct {
	Key       string  `json:"key"`
	Cloud     Cloud   `json:"cloud,omitempty"` // set on scope rows
	Evaluated int     `json:"evaluated"`
	Compliant int     `json:"compliant"`
	Percent   float64 `json:"percent"` // 0-100, one decimal; 100 when nothing was evaluated
}

// UnreadableScope is a scope tagherd could not read. It is never compliant.
type UnreadableScope struct {
	Cloud Cloud  `json:"cloud"`
	Scope string `json:"scope"`
	Error string `json:"error"`
}

// CoverageGap is a policy type with no discovery adapter in a scope.
type CoverageGap struct {
	Cloud Cloud  `json:"cloud"`
	Scope string `json:"scope"`
	Type  string `json:"type"`
}

// NewRate computes a rate, rounding the percentage to one decimal.
func NewRate(key string, evaluated, compliant int) Rate {
	p := 100.0
	if evaluated > 0 {
		p = math.Round(float64(compliant)/float64(evaluated)*1000) / 10
	}
	return Rate{Key: key, Evaluated: evaluated, Compliant: compliant, Percent: p}
}

// SortRates orders rates by cloud, then key.
func SortRates(rs []Rate) {
	slices.SortFunc(rs, func(a, b Rate) int {
		return cmp.Or(cmp.Compare(a.Cloud, b.Cloud), cmp.Compare(a.Key, b.Key))
	})
}
