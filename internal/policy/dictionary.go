package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
)

// AdminEmail is the one free-form tag with a format rule: a bare address.
const AdminEmail = "AdminEmail"

// TagDef is one tag in the dictionary. Allowed is nil for free-form tags.
type TagDef struct {
	Name    string   `json:"name"`
	Allowed []string `json:"allowed"`
}

// Enumerated reports whether the tag has a fixed list of values.
func (t *TagDef) Enumerated() bool { return t.Allowed != nil }

// Canonical returns the dictionary spelling of value, ignoring case.
func (t *TagDef) Canonical(value string) (string, bool) {
	for _, a := range t.Allowed {
		if strings.EqualFold(a, value) {
			return a, true
		}
	}
	return "", false
}

// Accept returns value in its dictionary spelling when it may be written:
// one of the allowed values ignoring case, a bare email address for
// AdminEmail, or any non-blank value for other free-form tags.
func (t *TagDef) Accept(value string) (string, bool) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return "", false
	case t.Enumerated():
		return t.Canonical(value)
	case t.Name == AdminEmail && !ValidEmail(value):
		return "", false
	}
	return value, true
}

// ValidEmail accepts a bare address such as ops@example.com, nothing more.
func ValidEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == ""
}

// Dictionary is the parsed tags.json, in file order.
type Dictionary struct {
	Tags   []*TagDef
	byName map[string]*TagDef
}

// Lookup finds a tag by name, ignoring case.
func (d *Dictionary) Lookup(name string) (*TagDef, bool) {
	t, ok := d.byName[strings.ToLower(name)]
	return t, ok
}

// parseDictionary decodes tags.json, keeping key order, and reports problems.
func parseDictionary(data []byte, r *Report) *Dictionary {
	const file = "tags.json"
	d := &Dictionary{byName: map[string]*TagDef{}}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		r.add(LevelError, file, "", "must be a JSON object of tag name to allowed values")
		return nil
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			r.add(LevelError, file, "", "invalid JSON: %v", err)
			return nil
		}
		name := tok.(string)
		var allowed []string
		if err := dec.Decode(&allowed); err != nil {
			r.add(LevelError, file, name, "must be null or a list of strings")
			continue
		}
		if strings.TrimSpace(name) == "" {
			r.add(LevelError, file, "", "tag name must not be empty")
			continue
		}
		if prev, dup := d.byName[strings.ToLower(name)]; dup {
			r.add(LevelError, file, name, "duplicates %q (tag names ignore case)", prev.Name)
			continue
		}
		if allowed != nil {
			if len(allowed) == 0 {
				r.add(LevelError, file, name, "allowed values must be null (any value) or a non-empty list")
				continue
			}
			seen := map[string]bool{}
			for i, v := range allowed {
				lv := strings.ToLower(v)
				switch {
				case strings.TrimSpace(v) == "":
					r.add(LevelError, file, fmt.Sprintf("%s[%d]", name, i), "allowed value must not be empty")
				case seen[lv]:
					r.add(LevelError, file, fmt.Sprintf("%s[%d]", name, i), "duplicate value %q (values ignore case)", v)
				}
				seen[lv] = true
			}
		}
		t := &TagDef{Name: name, Allowed: allowed}
		d.Tags = append(d.Tags, t)
		d.byName[strings.ToLower(name)] = t
	}
	if _, err := dec.Token(); err != nil {
		r.add(LevelError, file, "", "invalid JSON: %v", err)
		return nil
	}
	if len(d.Tags) == 0 {
		r.add(LevelError, file, "", "defines no tags")
	}
	return d
}
