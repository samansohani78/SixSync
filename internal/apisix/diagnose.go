package apisix

import (
	"context"
	"net/url"

	"github.com/samansohani78/SixSync/internal/model"
)

// KeyCapability describes what the configured credential is permitted to do.
type KeyCapability string

const (
	// CapabilityUnknown means the probe could not reach a conclusion.
	CapabilityUnknown KeyCapability = "unknown"

	// CapabilityNone means the credential was rejected outright.
	CapabilityNone KeyCapability = "none"

	// CapabilityReadOnly means the credential is a viewer key: it may read but
	// APISIX refuses any non-GET method.
	CapabilityReadOnly KeyCapability = "read-only"

	// CapabilityAdmin means the credential may write. SixSync issues no writes
	// today; this is reported so an operator can confirm least privilege.
	CapabilityAdmin KeyCapability = "administrator"
)

// Diagnosis is the result of probing an endpoint. Every field is safe to
// display: it contains no credential material.
type Diagnosis struct {
	// Endpoint is the normalized Admin API base URL.
	Endpoint string

	// Reachable reports whether an HTTP response was obtained at all.
	Reachable bool

	// Authenticated reports whether the credential was accepted for reads.
	Authenticated bool

	// Capability is what the credential may do.
	Capability KeyCapability

	// APISIXVersion is parsed from the Server response header, empty if it
	// could not be determined. A proxy may strip or rewrite that header, so
	// absence is not evidence that the endpoint is not APISIX.
	APISIXVersion string

	// AdminAPIVersion is the X-API-VERSION response header, e.g. "v3".
	AdminAPIVersion string

	// SchemaValidationAvailable reports whether the schema validation endpoint
	// answered, which `validate --remote` depends on.
	SchemaValidationAvailable bool

	// Problems are human-readable descriptions of what went wrong, in the
	// order encountered.
	Problems []string
}

// Diagnose probes an endpoint without mutating anything.
//
// Every request it makes is either a GET or the schema-validation POST, which
// APISIX implements as a pure function of its request body. Nothing here
// creates, updates or deletes a resource.
func (c *Client) Diagnose(ctx context.Context) Diagnosis {
	d := Diagnosis{Endpoint: c.baseURL, Capability: CapabilityUnknown}

	// Step 1: a plain list read. This establishes reachability, TLS validity
	// and whether the credential is accepted at all.
	routes, ok := model.Lookup(model.KindRoute)
	if !ok {
		d.Problems = append(d.Problems, "internal error: Route kind is not registered")
		return d
	}

	resp, err := c.do(ctx, "GET", "/"+routes.APIPath, url.Values{"page_size": []string{"10"}}, nil)
	if resp != nil {
		// A response arrived, so the endpoint is reachable even if it refused
		// the request.
		d.Reachable = true
		d.APISIXVersion = ServerVersion(resp.headers)
		d.AdminAPIVersion = resp.headers.Get("X-API-VERSION")
	}

	switch {
	case err == nil:
		d.Authenticated = true

	case IsKind(err, ErrTransport):
		d.Problems = append(d.Problems, "cannot reach the Admin API: "+err.Error())
		return d

	case IsKind(err, ErrAuthMissing):
		d.Capability = CapabilityNone
		d.Problems = append(d.Problems, "no API key was provided, and this endpoint requires one")
		return d

	case IsKind(err, ErrAuthInvalid):
		d.Capability = CapabilityNone
		d.Problems = append(d.Problems, "the API key was rejected by APISIX")
		return d

	default:
		d.Problems = append(d.Problems, "unexpected response when listing routes: "+err.Error())
		return d
	}

	// Step 2: determine capability without mutating anything.
	//
	// POST /apisix/admin/schema/validate/{kind} uses a write method but only
	// validates its body against a JSON schema — it touches no stored state.
	// APISIX checks the credential's role before dispatching, so a viewer key
	// is refused with 401 "invalid method for role viewer" while an
	// administrator key reaches the validator and gets a schema verdict.
	//
	// That difference is what distinguishes the two, at zero risk.
	err = c.ValidateSchema(ctx, routes, map[string]any{"uri": "/"})
	switch {
	case err == nil:
		// Reached the validator and the body passed.
		d.Capability = CapabilityAdmin
		d.SchemaValidationAvailable = true

	case IsKind(err, ErrValidation):
		// Also reached the validator; it simply rejected this body. Both
		// outcomes prove the credential was allowed to use a write method.
		d.Capability = CapabilityAdmin
		d.SchemaValidationAvailable = true

	case IsKind(err, ErrAuthInsufficient):
		d.Capability = CapabilityReadOnly
		// The endpoint exists; this key just may not use it.
		d.SchemaValidationAvailable = true

	case IsKind(err, ErrNotFound):
		d.Problems = append(d.Problems,
			"this endpoint has no schema validation API; `validate --remote` will not work")

	default:
		d.Problems = append(d.Problems, "could not determine key capability: "+err.Error())
	}

	return d
}
