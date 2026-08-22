package apisix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/samansohani78/SixSync/internal/model"
)

// Put writes a resource to a chosen identifier.
//
// PUT is the only write verb SixSync uses.
//
// Not POST: APISIX would generate the identifier, so identity would be decided
// by the gateway rather than by the repository, and the same file applied twice
// would create two resources.
//
// Not PATCH: on APISIX it merges maps rather than replacing them, so the result
// depends on prior state instead of on the file — `nodes` is merged, not
// replaced — and 3.18 fixed a bug where PATCH double-encrypted `encrypt_fields`
// on every call. A declarative tool needs the written state to be a function of
// the file alone.
//
// The request is not retried. A write whose response was lost may or may not
// have been applied, and repeating it blindly could overwrite a concurrent
// change that landed in between.
func (c *Client) Put(ctx context.Context, d model.Definition, id string, spec map[string]any) (Resource, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return Resource{}, fmt.Errorf("encoding %s/%s: %w", d.Kind, id, err)
	}

	path := "/" + d.APIPath + "/" + url.PathEscape(id)
	resp, err := c.do(ctx, "PUT", path, nil, body)
	if err != nil {
		return Resource{}, err
	}

	// A write response echoes the stored resource but, unlike a read, carries
	// no revision fields. Callers that need one re-read.
	var e envelope
	if err := json.Unmarshal(resp.body, &e); err != nil {
		return Resource{}, &Error{
			Kind: ErrProtocol, Method: "PUT", Path: path,
			Message: "write response is not valid JSON", err: err,
		}
	}
	if len(e.Value) == 0 {
		// Some APISIX versions answer a write with an empty body. That is not
		// an error; the caller verifies by reading the resource back.
		return Resource{}, nil
	}
	return e.toResource("PUT", path)
}

// Delete removes a resource.
//
// `force` is never sent. It bypasses APISIX's own reference checks, which are
// one of the few protections that exist below SixSync; a deletion that APISIX
// refuses is information, not an obstacle.
func (c *Client) Delete(ctx context.Context, d model.Definition, id string) error {
	path := "/" + d.APIPath + "/" + url.PathEscape(id)
	_, err := c.do(ctx, "DELETE", path, nil, nil)
	return err
}

// Exists reports whether a resource is present.
func (c *Client) Exists(ctx context.Context, d model.Definition, id string) (bool, error) {
	_, err := c.Get(ctx, d, id)
	switch {
	case err == nil:
		return true, nil
	case IsKind(err, ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}
