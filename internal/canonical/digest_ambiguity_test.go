package canonical

import (
	"encoding/json"
	"testing"
)

// A digest that two different values can share would let a plan be swapped for
// a different one that still verifies. Canonical JSON must therefore be
// unambiguous: no two distinct values may serialize to the same bytes.
func TestDigestDistinguishesValuesThatCouldBeConfused(t *testing.T) {
	cases := []struct {
		name string
		a, b any
	}{
		{
			name: "a string that looks like a number",
			a:    map[string]any{"v": json.Number("1")},
			b:    map[string]any{"v": "1"},
		},
		{
			name: "a string that looks like a boolean",
			a:    map[string]any{"v": true},
			b:    map[string]any{"v": "true"},
		},
		{
			name: "null against the string null",
			a:    map[string]any{"v": nil},
			b:    map[string]any{"v": "null"},
		},
		{
			name: "an integer against the same value as a float",
			a:    map[string]any{"v": json.Number("1")},
			b:    map[string]any{"v": json.Number("1.0")},
		},
		{
			name: "key and value concatenation cannot be confused",
			a:    map[string]any{"ab": "c"},
			b:    map[string]any{"a": "bc"},
		},
		{
			name: "a nested object against its flattened rendering",
			a:    map[string]any{"a": map[string]any{"b": "c"}},
			b:    map[string]any{"a": `{"b":"c"}`},
		},
		{
			name: "an empty object against an empty list",
			a:    map[string]any{"v": map[string]any{}},
			b:    map[string]any{"v": []any{}},
		},
		{
			name: "a list against an object with numeric keys",
			a:    map[string]any{"v": []any{"x"}},
			b:    map[string]any{"v": map[string]any{"0": "x"}},
		},
		{
			name: "a value containing the separator characters",
			a:    map[string]any{"a": "b\",\"c", "d": "e"},
			b:    map[string]any{"a": "b", "c\",\"d": "e"},
		},
		{
			name: "large integers one apart",
			a:    map[string]any{"v": json.Number("9007199254740993")},
			b:    map[string]any{"v": json.Number("9007199254740992")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			da, err := Digest(tc.a)
			if err != nil {
				t.Fatal(err)
			}
			db, err := Digest(tc.b)
			if err != nil {
				t.Fatal(err)
			}
			if da == db {
				am, _ := Marshal(tc.a)
				bm, _ := Marshal(tc.b)
				t.Errorf("two distinct values share a digest:\n  %s\n  %s\nboth %s", am, bm, da)
			}
		})
	}
}

// Equal values must produce equal digests regardless of construction order.
func TestDigestIsIndependentOfInsertionOrder(t *testing.T) {
	a := map[string]any{}
	for _, k := range []string{"z", "m", "a", "q", "b"} {
		a[k] = k
	}
	b := map[string]any{}
	for _, k := range []string{"b", "q", "a", "m", "z"} {
		b[k] = k
	}

	da, err := Digest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Digest(b)
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Errorf("digest depends on insertion order: %s vs %s", da, db)
	}
}

// A control character or a quote inside a key must not be able to forge the
// surrounding structure.
func TestDigestHandlesAdversarialKeys(t *testing.T) {
	cases := []map[string]any{
		{`a":"b`: "c"},
		{"a\nb": "c"},
		{"a\x00b": "c"},
		{"\\": "c"},
		{"": "empty key"},
	}
	seen := map[string]string{}
	for i, m := range cases {
		d, err := Digest(m)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if prev, dup := seen[d]; dup {
			t.Errorf("case %d shares a digest with %s", i, prev)
		}
		out, _ := Marshal(m)
		seen[d] = string(out)
	}
}
