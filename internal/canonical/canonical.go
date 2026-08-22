// Package canonical normalizes APISIX resource bodies so that two of them can
// be compared for semantic equality.
//
// There is deliberately no generic "walk the map and compare" path here. APISIX
// resources contain fields whose JSON representation varies without their
// meaning varying — upstream nodes accept two entirely different shapes, some
// lists are sets and others are ordered, and reference fields echo back
// whatever numeric type the writer used. A generic comparison reports all of
// that as drift, which is exactly the failure mode that makes a GitOps tool
// untrustworthy: an operator who sees phantom changes stops reading diffs.
//
// So every rule below is explicit and justified. When this package meets a
// shape it has no rule for, it returns an error rather than guessing, because a
// wrong diff is worse than a missing one.
//
// The rules were derived from apache/apisix:3.18.0-debian's observed behaviour.
// See docs/compatibility.md.
package canonical

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/samansohani78/SixSync/internal/model"
)

// serverOwnedFields are written by APISIX and cannot be set by a client:
// APISIX rejects a body containing them with
// `{"error_msg":"the property is forbidden:  create_time"}`.
//
// update_time additionally changes on every write, including one that stores
// byte-identical content, so comparing it would report drift after any
// unrelated touch.
var serverOwnedFields = []string{"create_time", "update_time"}

// setFields are list-valued fields whose order carries no meaning, per kind.
// Reordering these in APISIX changes nothing about request matching, so a
// reorder must not be reported as a change.
//
// Fields deliberately absent: `vars` (an ordered expression list) and
// `plugins` (a map, not a list).
var setFields = map[model.Kind][]string{
	model.KindRoute:       {"methods", "hosts", "uris", "remote_addrs"},
	model.KindService:     {"hosts"},
	model.KindStreamRoute: {},
	model.KindSSL:         {"snis"},
}

// Canonicalize returns a normalized copy of an APISIX resource body.
//
// The input is never modified. The result is suitable for comparison and for
// hashing, but is not what gets written to APISIX — reference fields are
// coerced to strings here for comparison purposes, and that coercion must not
// leak back onto the wire.
func Canonicalize(kind model.Kind, spec map[string]any) (map[string]any, error) {
	def, ok := model.Lookup(kind)
	if !ok {
		return nil, fmt.Errorf("cannot canonicalize unknown kind %q", kind)
	}

	out := make(map[string]any, len(spec))
	for k, v := range spec {
		nv, err := normalizeValue(v)
		if err != nil {
			return nil, fmt.Errorf("%s: field %q: %w", kind, k, err)
		}
		out[k] = nv
	}

	// Identity and server-owned bookkeeping are not part of the comparable
	// body. Identity lives in a resource file's metadata; timestamps belong to
	// the server.
	delete(out, def.IdentityField)
	for _, f := range serverOwnedFields {
		delete(out, f)
	}

	// Reference fields echo back whichever JSON type was submitted: APISIX
	// returns `"id":"1"` alongside `"upstream_id":1` in the same object. Only
	// the identifier matters, so compare them as strings.
	for _, ref := range def.References {
		v, present := out[ref.Field]
		if !present {
			continue
		}
		s, err := scalarToString(v)
		if err != nil {
			return nil, fmt.Errorf("%s: reference field %q: %w", kind, ref.Field, err)
		}
		out[ref.Field] = s
	}

	for _, f := range setFields[kind] {
		if v, present := out[f]; present {
			sorted, err := sortStringSet(v)
			if err != nil {
				return nil, fmt.Errorf("%s: field %q: %w", kind, f, err)
			}
			out[f] = sorted
		}
	}

	// Upstream node lists are the one genuinely polymorphic shape, and appear
	// both at the top level of an Upstream and inlined in a Route or Service.
	if err := canonicalizeNodesAt(out); err != nil {
		return nil, fmt.Errorf("%s: %w", kind, err)
	}
	if inline, ok := out["upstream"].(map[string]any); ok {
		if err := canonicalizeNodesAt(inline); err != nil {
			return nil, fmt.Errorf("%s: inline upstream: %w", kind, err)
		}
	}

	return out, nil
}

// normalizeValue deep-copies a decoded JSON value and normalizes its numbers.
func normalizeValue(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			nv, err := normalizeValue(val)
			if err != nil {
				return nil, err
			}
			out[k] = nv
		}
		return out, nil

	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			nv, err := normalizeValue(val)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil

	case json.Number:
		// Preserve the integer/float distinction, and preserve full precision
		// for integers beyond float64's exact range.
		return t, nil

	case float64:
		// Reached when a value was decoded without UseNumber, e.g. from a YAML
		// file. Re-express integral values as integers so that a route
		// priority written as 0 in YAML matches a 0 returned as JSON.
		if t == float64(int64(t)) {
			return json.Number(strconv.FormatInt(int64(t), 10)), nil
		}
		return json.Number(strconv.FormatFloat(t, 'f', -1, 64)), nil

	case int:
		return json.Number(strconv.Itoa(t)), nil
	case int64:
		return json.Number(strconv.FormatInt(t, 10)), nil
	case uint64:
		return json.Number(strconv.FormatUint(t, 10)), nil

	case string, bool, nil:
		return t, nil

	default:
		return nil, fmt.Errorf("unsupported value type %T", v)
	}
}

func scalarToString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	default:
		return "", fmt.Errorf("expected a string or number, got %T", v)
	}
}

func sortStringSet(v any) (any, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a list, got %T", v)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("expected a list of strings, found %T", item)
		}
		out = append(out, s)
	}
	sort.Strings(out)

	// Return as []any so the result stays a plain decoded-JSON value.
	res := make([]any, len(out))
	for i, s := range out {
		res[i] = s
	}
	return res, nil
}

// canonicalizeNodesAt rewrites a `nodes` field in place, if present.
//
// APISIX documents two equivalent encodings and returns whichever the writer
// used:
//
//	{"10.0.0.1:8080": 1}
//	[{"host":"10.0.0.1","port":8080,"weight":1}]
//
// Both are converted to the array form, sorted by host then port, so that the
// same set of backends compares equal regardless of how it was written. Sorting
// is safe because APISIX selects a node by the configured balancer, not by
// position — with the exception of `priority`, which is an explicit field and
// is preserved rather than implied by order.
func canonicalizeNodesAt(m map[string]any) error {
	raw, present := m["nodes"]
	if !present || raw == nil {
		return nil
	}

	var nodes []map[string]any

	switch t := raw.(type) {
	case map[string]any:
		for hostPort, weight := range t {
			n, err := nodeFromMapEntry(hostPort, weight)
			if err != nil {
				return err
			}
			nodes = append(nodes, n)
		}

	case []any:
		for _, item := range t {
			obj, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("nodes: expected each entry to be an object, got %T", item)
			}
			nodes = append(nodes, obj)
		}

	default:
		return fmt.Errorf("nodes: expected an object or a list, got %T", raw)
	}

	sort.SliceStable(nodes, func(i, j int) bool {
		hi, _ := nodes[i]["host"].(string)
		hj, _ := nodes[j]["host"].(string)
		if hi != hj {
			return hi < hj
		}
		return nodePort(nodes[i]) < nodePort(nodes[j])
	})

	out := make([]any, len(nodes))
	for i, n := range nodes {
		out[i] = n
	}
	m["nodes"] = out
	return nil
}

// nodeFromMapEntry converts one "host:port": weight pair to the object form.
//
// IPv6 hosts are written with brackets — "[::1]:80" — so the port is split at
// the last colon, and a host with no port is left without one rather than being
// given a fabricated default.
func nodeFromMapEntry(hostPort string, weight any) (map[string]any, error) {
	n := map[string]any{}

	host, portStr := hostPort, ""
	if idx := strings.LastIndex(hostPort, ":"); idx >= 0 {
		maybeHost, maybePort := hostPort[:idx], hostPort[idx+1:]
		// A colon inside an unbracketed IPv6 literal is not a port separator.
		if !strings.Contains(maybePort, "]") && maybePort != "" {
			if _, err := strconv.Atoi(maybePort); err == nil {
				host, portStr = maybeHost, maybePort
			}
		}
	}

	n["host"] = host
	if portStr != "" {
		n["port"] = json.Number(portStr)
	}

	w, err := normalizeValue(weight)
	if err != nil {
		return nil, fmt.Errorf("nodes[%q]: weight: %w", hostPort, err)
	}
	n["weight"] = w
	return n, nil
}

func nodePort(n map[string]any) int64 {
	switch p := n["port"].(type) {
	case json.Number:
		v, err := p.Int64()
		if err != nil {
			return 0
		}
		return v
	case float64:
		return int64(p)
	default:
		return 0
	}
}
