package canonical

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/model"
)

func decodeAny(s string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, false
	}
	return m, true
}

// FuzzCanonicalize checks that canonicalization never panics, is idempotent,
// and produces a stable digest.
//
// This runs over every Admin API response body SixSync reads, so a panic is a
// crash triggered by whatever a gateway returns.
func FuzzCanonicalize(f *testing.F) {
	f.Add(`{"uri":"/a","upstream_id":"u1"}`)
	f.Add(`{"nodes":{"10.0.0.1:80":1}}`)
	f.Add(`{"nodes":[{"host":"a","port":80,"weight":1}]}`)
	f.Add(`{"methods":["GET","POST"]}`)
	f.Add(`{"upstream_id":552218409505391294}`)
	f.Add(`{"nodes":{"[::1]:80":1}}`)
	f.Add(`{"a":null}`)
	f.Add(`{"a":{"b":{"c":{"d":{"e":1}}}}}`)
	f.Add(`{"nodes":{"":0}}`)
	f.Add(`{"upstream":{"nodes":{"h:1":1}}}`)

	kinds := []model.Kind{
		model.KindRoute, model.KindService, model.KindUpstream,
		model.KindPluginConfig, model.KindGlobalRule,
	}

	f.Fuzz(func(t *testing.T, body string) {
		spec, ok := decodeAny(body)
		if !ok {
			return
		}

		for _, kind := range kinds {
			first, err := Canonicalize(kind, spec)
			if err != nil {
				continue // refusing a shape it cannot compare is correct
			}

			// Canonicalizing twice must change nothing: a second pass over an
			// already-normalized value is what happens whenever a plan is
			// re-derived from stored state.
			second, err := Canonicalize(kind, first)
			if err != nil {
				t.Fatalf("%s: canonical output could not be re-canonicalized: %v", kind, err)
			}

			d1, err := Digest(first)
			if err != nil {
				t.Fatalf("%s: digesting canonical output: %v", kind, err)
			}
			d2, err := Digest(second)
			if err != nil {
				t.Fatalf("%s: digesting a second pass: %v", kind, err)
			}
			if d1 != d2 {
				t.Fatalf("%s: canonicalization is not idempotent: %s then %s", kind, d1, d2)
			}

			// The digest must not vary between calls on the same value.
			for i := 0; i < 3; i++ {
				again, err := Digest(first)
				if err != nil {
					t.Fatal(err)
				}
				if again != d1 {
					t.Fatalf("%s: digest varied between calls", kind)
				}
			}
		}
	})
}

// FuzzDiffFields checks that diffing never panics and is self-consistent.
func FuzzDiffFields(f *testing.F) {
	f.Add(`{"a":1}`, `{"a":2}`)
	f.Add(`{}`, `{"plugins":{"key-auth":{"key":"x"}}}`)
	f.Add(`{"nodes":[{"h":1}]}`, `{"nodes":[]}`)
	f.Add(`{"a":null}`, `{}`)
	f.Add(`{"a":{"b":[1,2,3]}}`, `{"a":{"b":[1,2]}}`)

	f.Fuzz(func(t *testing.T, observedBody, desiredBody string) {
		observed, ok1 := decodeAny(observedBody)
		desired, ok2 := decodeAny(desiredBody)
		if !ok1 || !ok2 {
			return
		}

		changes := DiffFields(observed, desired)

		// Identical inputs must produce no changes.
		if len(DiffFields(desired, desired)) != 0 {
			t.Fatal("a value diffed against itself produced changes")
		}

		// Ordering must be stable, since a diff is written into plan artifacts.
		for i := 0; i < 3; i++ {
			again := DiffFields(observed, desired)
			if len(again) != len(changes) {
				t.Fatal("diff length varied between calls")
			}
			for j := range again {
				if again[j] != changes[j] {
					t.Fatalf("diff ordering varied at index %d", j)
				}
			}
		}

		// No change may render a composite value, because everything inside
		// one would then sit at the parent's path where a secret rule cannot
		// match it. Empty composites are the exception: they have nothing
		// inside to leak, and `plugins: {cors: {}}` is meaningful.
		for _, c := range changes {
			for _, v := range []string{c.Old, c.New} {
				if v == "{}" || v == "[]" || v == "" {
					continue
				}
				if strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[") {
					// A string value can legitimately begin with a brace; only
					// a genuinely composite rendering is a problem, and those
					// parse as JSON containers.
					var probe any
					if json.Unmarshal([]byte(v), &probe) == nil {
						if _, isMap := probe.(map[string]any); isMap {
							t.Fatalf("a change renders a whole object at path %q, where nested "+
								"secrets cannot be redacted: %+v", c.Path, c)
						}
						if _, isList := probe.([]any); isList {
							t.Fatalf("a change renders a whole list at path %q: %+v", c.Path, c)
						}
					}
				}
			}
		}
	})
}

// FuzzMarshal checks the canonical serializer.
func FuzzMarshal(f *testing.F) {
	f.Add(`{"b":1,"a":2}`)
	f.Add(`{"n":552218409505391294}`)
	f.Add(`{"s":"a < b && c > d"}`)
	f.Add(`{"nested":{"deep":{"deeper":[1,"two",null,true]}}}`)

	f.Fuzz(func(t *testing.T, body string) {
		v, ok := decodeAny(body)
		if !ok {
			return
		}

		first, err := Marshal(v)
		if err != nil {
			return
		}

		// Byte-stable across calls, or golden files and digests mean nothing.
		for i := 0; i < 3; i++ {
			again, err := Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(first) {
				t.Fatalf("Marshal is not deterministic:\n%s\n%s", first, again)
			}
		}

		// The output must be valid JSON that round-trips to the same bytes.
		reparsed, ok := decodeAny(string(first))
		if !ok {
			t.Fatalf("Marshal produced invalid JSON: %s", first)
		}
		second, err := Marshal(reparsed)
		if err != nil {
			t.Fatal(err)
		}
		if string(second) != string(first) {
			t.Fatalf("Marshal does not round-trip:\n%s\n%s", first, second)
		}
	})
}
