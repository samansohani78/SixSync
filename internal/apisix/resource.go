package apisix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"github.com/samansohani78/SixSync/internal/model"
)

// Resource is one APISIX resource as read from the Admin API, with the
// transport envelope already removed.
type Resource struct {
	// Value is the resource body: exactly what APISIX stores, decoded but
	// otherwise untouched. Unknown fields are preserved, because APISIX gains
	// fields between releases and dropping them would be data loss.
	Value map[string]any

	// EtcdKey is the backing store key, e.g. "/apisix/routes/r1". Useful for
	// diagnostics; never written to a resource file.
	EtcdKey string

	// ModifiedIndex is the etcd revision at which this resource last changed.
	//
	// This is SixSync's concurrency token. APISIX offers no conditional write,
	// so it supports detect-then-abort rather than compare-and-swap: read the
	// index, and re-read it immediately before writing to confirm nothing
	// moved. It advances on *any* write, including one that stores identical
	// content, which makes it conservative in the safe direction.
	ModifiedIndex int64

	// CreatedIndex is the revision at which the resource first appeared.
	CreatedIndex int64
}

// ID returns the resource's identity, using the identity field appropriate to
// its kind. APISIX returns integer IDs as JSON numbers and string IDs as
// strings, so both are normalized to string here.
func (r Resource) ID(d model.Definition) (string, error) {
	v, ok := r.Value[d.IdentityField]
	if !ok {
		return "", fmt.Errorf("resource at %s has no %q field", r.EtcdKey, d.IdentityField)
	}
	s, err := scalarToString(v)
	if err != nil {
		return "", fmt.Errorf("resource at %s: %q: %w", r.EtcdKey, d.IdentityField, err)
	}
	if s == "" {
		return "", fmt.Errorf("resource at %s has an empty %q", r.EtcdKey, d.IdentityField)
	}
	return s, nil
}

// scalarToString normalizes the identifier types APISIX actually emits.
//
// json.Unmarshal into `any` yields float64 for every number, so an integer ID
// must be reconstructed rather than formatted as a float — otherwise ID 1
// becomes "1e+00" and a large one loses precision.
func scalarToString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), nil
		}
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case int:
		return strconv.Itoa(t), nil
	default:
		return "", fmt.Errorf("expected a string or number, got %T", v)
	}
}

// envelope is the single-resource response shape.
//
// Verified against apache/apisix:3.18.0-debian, which advertises
// X-API-VERSION: v3 with no configuration:
//
//	{"createdIndex":28,"value":{…},"key":"/apisix/upstreams/u1","modifiedIndex":28}
type envelope struct {
	Key           string          `json:"key"`
	Value         json.RawMessage `json:"value"`
	CreatedIndex  int64           `json:"createdIndex"`
	ModifiedIndex int64           `json:"modifiedIndex"`
}

// listEnvelope is the list response shape: {"list":[…],"total":N}.
//
// List is held as raw JSON because APISIX does not always send an array. When
// a paginated request matches nothing, the empty Lua table behind the response
// serializes as an object: `GET /apisix/admin/ssls?page=1&page_size=500` on an
// empty collection returns {"list":{},"total":0}. Decoding straight into a
// slice fails there, which would break every command against a gateway that
// has no resources of some kind — precisely the first-run experience.
type listEnvelope struct {
	List  json.RawMessage `json:"list"`
	Total int             `json:"total"`
}

// items decodes the list payload, tolerating the empty-object form.
func (le listEnvelope) items() ([]envelope, error) {
	trimmed := bytes.TrimSpace(le.List)

	// Absent, null, or the empty-table-as-object form: no items.
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return nil, nil
	}

	var out []envelope
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e envelope) toResource(method, path string) (Resource, error) {
	if len(e.Value) == 0 {
		return Resource{}, &Error{
			Kind: ErrProtocol, Method: method, Path: path,
			Message: "response envelope has no \"value\" field",
		}
	}
	// Decoded with UseNumber so that integers survive intact. Plain
	// json.Unmarshal would turn every number into a float64, and an APISIX
	// reference such as upstream_id 552218409505391294 exceeds float64's
	// 2^53 exact-integer range — it would come back subtly corrupted, and
	// SixSync would then write that corrupted value back.
	dec := json.NewDecoder(bytes.NewReader(e.Value))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return Resource{}, &Error{
			Kind: ErrProtocol, Method: method, Path: path,
			Message: "response \"value\" is not a JSON object", err: err,
		}
	}
	return Resource{
		Value:         v,
		EtcdKey:       e.Key,
		ModifiedIndex: e.ModifiedIndex,
		CreatedIndex:  e.CreatedIndex,
	}, nil
}

// Get reads a single resource by ID.
func (c *Client) Get(ctx context.Context, d model.Definition, id string) (Resource, error) {
	path := "/" + d.APIPath + "/" + url.PathEscape(id)

	resp, err := c.do(ctx, "GET", path, nil, nil)
	if err != nil {
		return Resource{}, err
	}

	var e envelope
	if err := json.Unmarshal(resp.body, &e); err != nil {
		return Resource{}, &Error{
			Kind: ErrProtocol, Method: "GET", Path: path,
			Message: "response is not valid JSON", err: err,
		}
	}
	return e.toResource("GET", path)
}

// listPageSize is the page size used when listing.
//
// APISIX clamps page_size to [10,500]; 500 minimizes round trips on the large
// route tables this tool is built for.
const listPageSize = 500

// maxListPages bounds pagination so that a server which never advances the
// page can not spin forever.
const maxListPages = 10000

// List reads every resource of a kind, following pagination.
//
// Results are sorted by etcd key so that repeated calls produce identical
// ordering. APISIX does not promise a stable order, and every command that
// consumes this must be deterministic.
//
// Pagination is not atomic: a concurrent write between pages can cause a
// resource to be missed or seen twice. SixSync is read-only today, so this
// surfaces at worst as a drift report that a re-run corrects.
func (c *Client) List(ctx context.Context, d model.Definition) ([]Resource, error) {
	path := "/" + d.APIPath

	var out []Resource
	seen := make(map[string]struct{})

	for page := 1; page <= maxListPages; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(listPageSize))

		resp, err := c.do(ctx, "GET", path, q, nil)
		if err != nil {
			return nil, err
		}

		var le listEnvelope
		if err := json.Unmarshal(resp.body, &le); err != nil {
			return nil, &Error{
				Kind: ErrProtocol, Method: "GET", Path: path,
				Message: "list response is not valid JSON", err: err,
			}
		}

		items, err := le.items()
		if err != nil {
			return nil, &Error{
				Kind: ErrProtocol, Method: "GET", Path: path,
				Message: "list response has an unexpected \"list\" field", err: err,
			}
		}

		for _, e := range items {
			r, err := e.toResource("GET", path)
			if err != nil {
				return nil, err
			}
			// A resource can legitimately repeat across pages if a concurrent
			// write shifts the underlying ordering. Keep the first sighting
			// rather than emitting a duplicate.
			if _, dup := seen[r.EtcdKey]; dup {
				continue
			}
			seen[r.EtcdKey] = struct{}{}
			out = append(out, r)
		}

		// A short page means this was the last one.
		if len(items) < listPageSize {
			break
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].EtcdKey < out[j].EtcdKey })
	return out, nil
}

// ValidateSchema asks APISIX to check a resource body against its JSON schema.
//
// This mutates nothing: it is a pure validation endpoint that happens to use
// POST. It returns nil when the body is valid, and an ErrValidation error
// carrying APISIX's message when it is not.
//
// The check is structural only. APISIX does not verify that referenced
// resources exist, nor run per-plugin validation here, so a body that passes
// can still be rejected by a real write.
func (c *Client) ValidateSchema(ctx context.Context, d model.Definition, spec map[string]any) error {
	body, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encoding %s for validation: %w", d.Kind, err)
	}
	_, err = c.do(ctx, "POST", "/schema/validate/"+d.APIPath, nil, body)
	return err
}
