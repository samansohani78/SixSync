package apisix

import (
	"errors"
	"fmt"
)

// ErrorKind classifies an Admin API failure so callers can branch on the cause
// rather than on a status code or a message substring.
type ErrorKind string

const (
	// ErrTransport covers anything that prevented a response: DNS, connection
	// refused, TLS failure, timeout, context cancellation.
	ErrTransport ErrorKind = "transport"

	// ErrAuthMissing means no credential was presented.
	ErrAuthMissing ErrorKind = "auth-missing"

	// ErrAuthInvalid means the credential was not recognised.
	ErrAuthInvalid ErrorKind = "auth-invalid"

	// ErrAuthInsufficient means the credential is valid but its role does not
	// permit this method. On APISIX this is a viewer key attempting a write.
	//
	// APISIX reports all three auth outcomes as 401 and distinguishes them
	// only in the response's "description" field, so this classification is
	// necessarily message-based. See docs/compatibility.md.
	ErrAuthInsufficient ErrorKind = "auth-insufficient"

	// ErrNotFound means the resource does not exist.
	ErrNotFound ErrorKind = "not-found"

	// ErrValidation means APISIX rejected the request body.
	ErrValidation ErrorKind = "validation"

	// ErrReferenced means a delete was refused because another resource still
	// references the target.
	ErrReferenced ErrorKind = "referenced"

	// ErrConflict means APISIX's internal compare-and-swap lost a race. It is
	// reported as a 503, which is indistinguishable from a transient outage
	// without inspecting the message.
	ErrConflict ErrorKind = "conflict"

	// ErrServer is any other non-2xx response.
	ErrServer ErrorKind = "server"

	// ErrProtocol means the response was not shaped as the Admin API contract
	// requires — unparseable, or missing its envelope.
	ErrProtocol ErrorKind = "protocol"
)

// Error is a classified Admin API failure.
//
// Message holds APISIX's own text. It is safe to display: Admin API error
// messages describe schema problems and reference conflicts, and are not known
// to echo request bodies. The request body itself is never included here,
// because for a mutation it could contain credentials.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Method     string
	Path       string
	Message    string
	err        error
}

func (e *Error) Error() string {
	switch {
	case e.StatusCode == 0 && e.err != nil:
		return fmt.Sprintf("%s %s: %s: %v", e.Method, e.Path, e.Kind, e.err)
	case e.Message != "":
		return fmt.Sprintf("%s %s: %s (HTTP %d): %s", e.Method, e.Path, e.Kind, e.StatusCode, e.Message)
	default:
		return fmt.Sprintf("%s %s: %s (HTTP %d)", e.Method, e.Path, e.Kind, e.StatusCode)
	}
}

func (e *Error) Unwrap() error { return e.err }

// IsKind reports whether err is an Admin API error of the given kind.
func IsKind(err error, k ErrorKind) bool {
	var ae *Error
	if errors.As(err, &ae) {
		return ae.Kind == k
	}
	return false
}
