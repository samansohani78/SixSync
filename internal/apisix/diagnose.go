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
	// The probe is a DELETE against an identifier APISIX can never accept:
	// it contains a space, and the id schema rejects that on write. No
	// resource can therefore ever carry this identifier, so the request
	// cannot destroy anything — confirmed against 3.16.0 and 3.18.0, both of
	// which reject `PUT /routes/with%20space` with
	// `property "id" validation failed`.
	//
	// APISIX checks the credential's role before dispatching, so a viewer key
	// is refused with 401 "invalid method for role viewer" and never reaches
	// storage, while an administrator key reaches the handler and gets 404
	// "Key not found" or a 400 for the malformed identifier. That difference
	// is what distinguishes the two.
	//
	// An earlier version probed POST /apisix/admin/schema/validate/{kind}
	// instead. That endpoint is not role-gated on APISIX 3.16.0 — a viewer key
	// receives a schema verdict rather than 401 — so the probe reported every
	// viewer key on 3.16 as an administrator. APISIX 3.17.0 began requiring
	// the admin key there (#13328). The DELETE probe behaves identically on
	// every supported version.
	err = c.probeCapability(ctx, routes)
	switch {
	case IsKind(err, ErrAuthInsufficient):
		d.Capability = CapabilityReadOnly
	case IsKind(err, ErrNotFound), IsKind(err, ErrValidation), err == nil:
		// The request reached the handler, so this credential may use a write
		// method. Nothing was deleted: the identifier cannot exist.
		d.Capability = CapabilityAdmin
	case IsKind(err, ErrAuthInvalid), IsKind(err, ErrAuthMissing):
		d.Capability = CapabilityNone
		d.Problems = append(d.Problems, "the API key was rejected when probing write capability")
	default:
		d.Problems = append(d.Problems, "could not determine key capability: "+err.Error())
	}

	// Step 3: report whether remote schema validation is usable, which
	// `validate --remote` depends on. This is a separate question from write
	// capability, because the endpoint is not role-gated on every version.
	err = c.ValidateSchema(ctx, routes, map[string]any{"uri": "/"})
	switch {
	case err == nil, IsKind(err, ErrValidation):
		d.SchemaValidationAvailable = true
	case IsKind(err, ErrAuthInsufficient):
		// The endpoint exists; this key may not use it.
		d.SchemaValidationAvailable = true
		d.Problems = append(d.Problems,
			"this key cannot use remote schema validation, because APISIX exposes it "+
				"only through a write method; `validate --remote` needs an administrator key")
	case IsKind(err, ErrNotFound):
		d.Problems = append(d.Problems,
			"this endpoint has no schema validation API; `validate --remote` will not work")
	default:
		d.Problems = append(d.Problems, "could not reach schema validation: "+err.Error())
	}

	return d
}

// CapabilityProbeID is an identifier APISIX can never store.
//
// The space is the point: APISIX's id schema accepts only [a-zA-Z0-9._-], so a
// resource with this identifier cannot be created through the Admin API, and a
// DELETE against it cannot remove anything that exists.
const CapabilityProbeID = "sixsync capability probe"

// probeCapability issues the non-destructive write-method request described in
// Diagnose and returns its classified error.
func (c *Client) probeCapability(ctx context.Context, d model.Definition) error {
	_, err := c.do(ctx, "DELETE", "/"+d.APIPath+"/"+url.PathEscape(CapabilityProbeID), nil, nil)
	return err
}
