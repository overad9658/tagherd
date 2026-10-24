// Package output renders results and maps outcomes to exit codes.
package output

import (
	"errors"
	"fmt"
)

// Exit codes, as defined in the CLI spec. When several apply, the highest wins.
const (
	ExitOK          = 0 // success
	ExitFindings    = 1 // findings in the --fail-on classes
	ExitUsage       = 2 // usage, configuration or policy error; nothing scanned or written
	ExitIncomplete  = 3 // at least one scope could not be read
	ExitApplyFailed = 4 // at least one apply change failed or was stale
)

// ErrUsage marks command-line mistakes.
var ErrUsage = errors.New("usage error")

// Usagef returns an error wrapping ErrUsage.
func Usagef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, args...))
}

// ExitError carries an explicit exit code for outcomes that are not
// failures of tagherd itself, such as findings or an incomplete scan.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode maps an error returned by a command to the process exit code.
// Errors without an explicit code are usage, configuration or policy errors.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitUsage
}
