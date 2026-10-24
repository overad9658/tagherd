package policy

import (
	"errors"
	"fmt"
	"sort"
)

// ErrInvalid is wrapped by Report.Err when the policy has errors.
var ErrInvalid = errors.New("policy is invalid")

// Level is the severity of a diagnostic.
type Level string

const (
	LevelError   Level = "error"
	LevelWarning Level = "warning"
	LevelInfo    Level = "info"
)

// Diagnostic is one finding from loading or validating the policy.
type Diagnostic struct {
	Level   Level  `json:"level"`
	File    string `json:"file"`
	Path    string `json:"path,omitempty"` // JSON path inside the file
	Message string `json:"message"`
}

func (d Diagnostic) String() string {
	loc := d.File
	if d.Path != "" {
		loc += ": " + d.Path
	}
	return fmt.Sprintf("%s: %s: %s", d.Level, loc, d.Message)
}

// Report collects diagnostics in a stable order.
type Report struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
}

func (r *Report) add(l Level, file, path, format string, args ...any) {
	r.Diagnostics = append(r.Diagnostics, Diagnostic{l, file, path, fmt.Sprintf(format, args...)})
}

// Count returns how many diagnostics have the given level.
func (r *Report) Count(l Level) int {
	n := 0
	for _, d := range r.Diagnostics {
		if d.Level == l {
			n++
		}
	}
	return n
}

// Err returns an error wrapping ErrInvalid when there are errors, or when
// strict is set and there are warnings.
func (r *Report) Err(strict bool) error {
	errs, warns := r.Count(LevelError), r.Count(LevelWarning)
	if errs > 0 || (strict && warns > 0) {
		return fmt.Errorf("%w: %d error(s), %d warning(s)", ErrInvalid, errs, warns)
	}
	return nil
}

var levelRank = map[Level]int{LevelError: 0, LevelWarning: 1, LevelInfo: 2}

func (r *Report) sort() {
	sort.SliceStable(r.Diagnostics, func(i, j int) bool {
		a, b := r.Diagnostics[i], r.Diagnostics[j]
		if levelRank[a.Level] != levelRank[b.Level] {
			return levelRank[a.Level] < levelRank[b.Level]
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Path < b.Path
	})
}
