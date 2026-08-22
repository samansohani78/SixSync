// Package exitcode defines SixSync's exit-code contract.
//
// These codes are part of the public interface: CI pipelines branch on them,
// so they must stay stable across releases. Codes reserved for commands that
// do not exist yet are declared here anyway, so that a future command cannot
// quietly claim a number that scripts already treat as meaning something else.
package exitcode

import "errors"

// Code is a process exit status.
type Code int

const (
	// OK means the command succeeded and, for comparison commands, found no
	// differences.
	OK Code = 0

	// Failure is the generic error: unreachable endpoint, authentication
	// failure, malformed input, validation error, I/O error.
	Failure Code = 1

	// Diff means the command completed successfully and found differences.
	// It is only ever returned when --detailed-exitcode is set, so that the
	// default behaviour of "differences are not an error" is preserved.
	Diff Code = 2

	// Conflict means the observed remote state no longer matches what a plan
	// was built against, so applying it would be unsafe.
	//
	// RESERVED: no command returns this yet. Introduced with `apply` (M3).
	Conflict Code = 3

	// PolicyDenied means a policy rule rejected the operation.
	//
	// RESERVED: no command returns this yet. Introduced with policy profiles (M2).
	PolicyDenied Code = 4

	// RollbackIncomplete means a mutation failed and the subsequent rollback
	// did not fully restore the prior state. Live state is mixed and needs
	// operator attention.
	//
	// RESERVED: no command returns this yet. Introduced with `apply` (M3).
	RollbackIncomplete Code = 5

	// Usage means the command line itself was wrong: unknown flag, missing
	// required argument, bad flag value. Matches the BSD sysexits EX_USAGE
	// convention so it cannot be confused with a runtime failure.
	Usage Code = 64
)

// String returns a short stable label, used in --output json and in tests.
func (c Code) String() string {
	switch c {
	case OK:
		return "ok"
	case Failure:
		return "failure"
	case Diff:
		return "diff"
	case Conflict:
		return "conflict"
	case PolicyDenied:
		return "policy-denied"
	case RollbackIncomplete:
		return "rollback-incomplete"
	case Usage:
		return "usage"
	default:
		return "unknown"
	}
}

// Error is an error that carries the exit code the process should end with.
// Commands return these; only main translates one into an os.Exit.
type Error struct {
	Code Code
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Wrap attaches an exit code to an error. It returns nil for a nil error so
// it can be used directly in a return statement.
func Wrap(c Code, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: c, Err: err}
}

// From reports the exit code an error should produce. A nil error is OK; an
// error with no explicit code is a generic Failure. This is the single place
// that decision is made.
func From(err error) Code {
	if err == nil {
		return OK
	}
	var ec *Error
	if errors.As(err, &ec) {
		return ec.Code
	}
	return Failure
}
