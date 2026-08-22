package canonical

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/model"
)

// decode parses JSON the way the Admin API client does, preserving numbers.
func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decoding test input: %v", err)
	}
	return m
}

func canon(t *testing.T, kind model.Kind, s string) map[string]any {
	t.Helper()
	got, err := Canonicalize(kind, decode(t, s))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	return got
}

// equivalent asserts that two encodings of the same meaning canonicalize
// identically. This is the property the whole package exists to provide.
func equivalent(t *testing.T, kind model.Kind, a, b string) {
	t.Helper()
	da, err := Digest(canon(t, kind, a))
	if err != nil {
		t.Fatal(err)
	}
	db, err := Digest(canon(t, kind, b))
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Errorf("expected these to canonicalize identically but they did not:\n A: %s\n B: %s\n %s vs %s",
			a, b, da, db)
	}
}

func differs(t *testing.T, kind model.Kind, a, b string) {
	t.Helper()
	da, _ := Digest(canon(t, kind, a))
	db, _ := Digest(canon(t, kind, b))
	if da == db {
		t.Errorf("expected these to differ but they canonicalized identically:\n A: %s\n B: %s", a, b)
	}
}

func TestServerOwnedFieldsAreRemoved(t *testing.T) {
	got := canon(t, model.KindRoute,
		`{"uri":"/x","upstream_id":"u1","id":"r1","create_time":1787430328,"update_time":1787430330}`)

	for _, f := range []string{"create_time", "update_time", "id"} {
		if _, present := got[f]; present {
			t.Errorf("field %q survived canonicalization; it is not part of the comparable body", f)
		}
	}
	if got["uri"] != "/x" {
		t.Errorf("uri was lost: %#v", got)
	}
}

// APISIX rewrites update_time on every write, including one storing identical
// content. Comparing it would report drift after any unrelated touch.
func TestTimestampChangeIsNotDrift(t *testing.T) {
	equivalent(t, model.KindRoute,
		`{"uri":"/x","upstream_id":"u1","id":"r1","create_time":1787430328,"update_time":1787430328}`,
		`{"uri":"/x","upstream_id":"u1","id":"r1","create_time":1787430328,"update_time":1787999999}`)
}

// The observed inconsistency: APISIX returns "id":"1" next to "upstream_id":1.
func TestReferenceFieldsCompareAsStrings(t *testing.T) {
	equivalent(t, model.KindRoute,
		`{"uri":"/x","upstream_id":1}`,
		`{"uri":"/x","upstream_id":"1"}`)

	// But genuinely different references must still differ.
	differs(t, model.KindRoute,
		`{"uri":"/x","upstream_id":1}`,
		`{"uri":"/x","upstream_id":2}`)
}

// float64 cannot hold this exactly; a naive decode corrupts it silently.
func TestLargeIntegerReferencesKeepFullPrecision(t *testing.T) {
	const big = "552218409505391294"
	got := canon(t, model.KindRoute, `{"uri":"/x","upstream_id":`+big+`}`)
	if got["upstream_id"] != big {
		t.Errorf("upstream_id = %v, want %s — precision was lost", got["upstream_id"], big)
	}
	differs(t, model.KindRoute,
		`{"uri":"/x","upstream_id":552218409505391294}`,
		`{"uri":"/x","upstream_id":552218409505391295}`)
}

func TestSetFieldsIgnoreOrder(t *testing.T) {
	equivalent(t, model.KindRoute,
		`{"uri":"/x","methods":["GET","POST","OPTIONS"],"upstream_id":"u1"}`,
		`{"uri":"/x","methods":["OPTIONS","GET","POST"],"upstream_id":"u1"}`)

	equivalent(t, model.KindRoute,
		`{"uris":["/a","/b"],"hosts":["b.example.test","a.example.test"],"upstream_id":"u1"}`,
		`{"uris":["/b","/a"],"hosts":["a.example.test","b.example.test"],"upstream_id":"u1"}`)

	// A genuinely different method set is still a change.
	differs(t, model.KindRoute,
		`{"uri":"/x","methods":["GET"],"upstream_id":"u1"}`,
		`{"uri":"/x","methods":["GET","POST"],"upstream_id":"u1"}`)
}

// `vars` is an ordered expression list: reordering it changes evaluation.
func TestOrderedFieldsRespectOrder(t *testing.T) {
	differs(t, model.KindRoute,
		`{"uri":"/x","upstream_id":"u1","vars":[["arg_a","==","1"],["arg_b","==","2"]]}`,
		`{"uri":"/x","upstream_id":"u1","vars":[["arg_b","==","2"],["arg_a","==","1"]]}`)
}

// The two documented node encodings mean the same thing.
func TestNodeMapAndArrayFormsAreEquivalent(t *testing.T) {
	equivalent(t, model.KindUpstream,
		`{"type":"roundrobin","nodes":{"10.0.0.1:8080":1}}`,
		`{"type":"roundrobin","nodes":[{"host":"10.0.0.1","port":8080,"weight":1}]}`)
}

func TestNodeOrderIsNormalized(t *testing.T) {
	equivalent(t, model.KindUpstream,
		`{"type":"roundrobin","nodes":[{"host":"10.0.0.2","port":80,"weight":1},{"host":"10.0.0.1","port":80,"weight":1}]}`,
		`{"type":"roundrobin","nodes":[{"host":"10.0.0.1","port":80,"weight":1},{"host":"10.0.0.2","port":80,"weight":1}]}`)
}

func TestNodeWeightChangeIsDrift(t *testing.T) {
	differs(t, model.KindUpstream,
		`{"type":"roundrobin","nodes":{"10.0.0.1:8080":1}}`,
		`{"type":"roundrobin","nodes":{"10.0.0.1:8080":5}}`)
}

func TestNodeHostPortSplitting(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantHost string
		wantPort string
	}{
		{"ipv4 with port", `{"nodes":{"10.0.0.1:8080":1}}`, "10.0.0.1", "8080"},
		{"hostname with port", `{"nodes":{"svc.internal.test:443":1}}`, "svc.internal.test", "443"},
		{"bracketed ipv6 with port", `{"nodes":{"[::1]:80":1}}`, "[::1]", "80"},
		{"host with no port", `{"nodes":{"svc.internal.test":1}}`, "svc.internal.test", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canon(t, model.KindUpstream, tt.in)
			nodes := got["nodes"].([]any)
			if len(nodes) != 1 {
				t.Fatalf("expected one node, got %d", len(nodes))
			}
			n := nodes[0].(map[string]any)
			if n["host"] != tt.wantHost {
				t.Errorf("host = %v, want %q", n["host"], tt.wantHost)
			}
			if tt.wantPort == "" {
				if _, present := n["port"]; present {
					t.Errorf("port = %v, want it absent (none was specified)", n["port"])
				}
				return
			}
			if got := n["port"].(json.Number).String(); got != tt.wantPort {
				t.Errorf("port = %v, want %q", got, tt.wantPort)
			}
		})
	}
}

func TestInlineUpstreamNodesAreCanonicalized(t *testing.T) {
	equivalent(t, model.KindRoute,
		`{"uri":"/x","upstream":{"type":"roundrobin","nodes":{"10.0.0.1:80":1}}}`,
		`{"uri":"/x","upstream":{"type":"roundrobin","nodes":[{"host":"10.0.0.1","port":80,"weight":1}]}}`)
}

// The whole point: since APISIX 3.16 does not write defaults back, a field the
// user never set must stay absent rather than being invented.
func TestDefaultsAreNotInvented(t *testing.T) {
	got := canon(t, model.KindRoute, `{"uri":"/probe","upstream_id":"u-probe","id":"r-probe"}`)

	for _, f := range []string{"priority", "status"} {
		if _, present := got[f]; present {
			t.Errorf("canonicalization invented a default for %q; APISIX 3.16+ does not store one", f)
		}
	}
	if len(got) != 2 {
		t.Errorf("expected exactly uri and upstream_id, got %#v", got)
	}
}

// An explicitly-set value that happens to equal the schema default is a real,
// user-authored value and must be preserved.
func TestExplicitValueEqualToDefaultIsPreserved(t *testing.T) {
	got := canon(t, model.KindRoute, `{"uri":"/x","upstream_id":"u1","priority":0}`)
	if _, present := got["priority"]; !present {
		t.Error("an explicitly-set priority of 0 was dropped")
	}
	differs(t, model.KindRoute,
		`{"uri":"/x","upstream_id":"u1"}`,
		`{"uri":"/x","upstream_id":"u1","priority":0}`)
}

func TestCanonicalizeDoesNotMutateInput(t *testing.T) {
	in := decode(t, `{"uri":"/x","id":"r1","methods":["POST","GET"],"upstream_id":1,"create_time":1}`)
	before, _ := json.Marshal(in)

	if _, err := Canonicalize(model.KindRoute, in); err != nil {
		t.Fatal(err)
	}

	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Errorf("input was mutated:\nbefore %s\nafter  %s", before, after)
	}
}

func TestUnknownFieldsArePreserved(t *testing.T) {
	// APISIX gains fields between releases; dropping them would be data loss.
	got := canon(t, model.KindRoute, `{"uri":"/x","upstream_id":"u1","some_future_field":{"a":1}}`)
	if _, present := got["some_future_field"]; !present {
		t.Error("an unrecognized field was dropped instead of preserved")
	}
}

func TestUnknownKindIsRefused(t *testing.T) {
	_, err := Canonicalize(model.Kind("Nonsense"), map[string]any{})
	if err == nil {
		t.Fatal("expected an error for an unregistered kind")
	}
}

// Guessing at an unrecognized shape would produce a confidently wrong diff.
func TestMalformedShapesAreRefusedNotGuessed(t *testing.T) {
	tests := []struct {
		name string
		kind model.Kind
		in   string
	}{
		{"nodes as a string", model.KindUpstream, `{"nodes":"10.0.0.1:80"}`},
		{"nodes array of scalars", model.KindUpstream, `{"nodes":[1,2,3]}`},
		{"methods not a list", model.KindRoute, `{"uri":"/x","methods":"GET"}`},
		{"methods list of non-strings", model.KindRoute, `{"uri":"/x","methods":[1,2]}`},
		{"reference field is an object", model.KindRoute, `{"uri":"/x","upstream_id":{"a":1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Canonicalize(tt.kind, decode(t, tt.in))
			if err == nil {
				t.Error("expected an explicit error rather than a silent guess")
			}
		})
	}
}

func TestDigestIsStableAcrossRuns(t *testing.T) {
	spec := canon(t, model.KindUpstream,
		`{"type":"roundrobin","nodes":{"10.0.0.3:80":1,"10.0.0.1:80":1,"10.0.0.2:80":1},"retries":3}`)

	first, err := Digest(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Map iteration order in Go is randomized per range; 200 runs makes an
	// order-dependent digest overwhelmingly likely to be caught.
	for i := 0; i < 200; i++ {
		got, err := Digest(spec)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("digest varied between runs: %s then %s", first, got)
		}
	}
}

func TestMarshalIsDeterministicAndSorted(t *testing.T) {
	in := map[string]any{"zebra": "z", "alpha": "a", "middle": "m"}
	first, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != `{"alpha":"a","middle":"m","zebra":"z"}` {
		t.Errorf("keys are not sorted: %s", first)
	}
	for i := 0; i < 100; i++ {
		again, _ := Marshal(in)
		if !bytes.Equal(first, again) {
			t.Fatalf("serialization varied: %s then %s", first, again)
		}
	}
}

// Golden files are only useful if a human can read them.
func TestMarshalDoesNotEscapeHTML(t *testing.T) {
	got, err := Marshal(map[string]any{"expr": "a < b && c > d"})
	if err != nil {
		t.Fatal(err)
	}
	for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(string(got), escaped) {
			t.Errorf("HTML escaping leaked into canonical JSON: %s", got)
		}
	}
	if want := `{"expr":"a < b && c > d"}`; string(got) != want {
		t.Errorf("Marshal = %s, want %s", got, want)
	}
}

func TestMarshalPreservesNumberForm(t *testing.T) {
	in := map[string]any{
		"int":   json.Number("3"),
		"float": json.Number("0.5"),
		"big":   json.Number("552218409505391294"),
	}
	got, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"big":552218409505391294,"float":0.5,"int":3}`
	if string(got) != want {
		t.Errorf("Marshal = %s, want %s", got, want)
	}
}

func TestCanonicalizeIsIdempotent(t *testing.T) {
	once := canon(t, model.KindUpstream, `{"type":"roundrobin","nodes":{"10.0.0.1:80":1}}`)
	twice, err := Canonicalize(model.KindUpstream, once)
	if err != nil {
		t.Fatalf("second pass failed: %v", err)
	}
	if !reflect.DeepEqual(once, twice) {
		t.Errorf("canonicalization is not idempotent:\n once: %#v\ntwice: %#v", once, twice)
	}
}
