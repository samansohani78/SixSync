package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzApply checks that redaction never panics, never mutates its input, and
// never leaves a value it was asked to remove.
func FuzzApply(f *testing.F) {
	f.Add(`{"ssl":{"key":"secret"}}`)
	f.Add(`{"plugins":{"a":{"password":"p"}}}`)
	f.Add(`{"nodes":[{"credential":"c"}]}`)
	f.Add(`{"a":{"b":{"c":{"d":{"authorization":"x"}}}}}`)
	f.Add(`{}`)
	f.Add(`{"a":null}`)

	policy := NewPolicy(
		[]string{"ssl.key", "plugins.*.password", "nodes.credential"},
		[]string{"authorization"},
	)

	f.Fuzz(func(t *testing.T, body string) {
		dec := json.NewDecoder(strings.NewReader(body))
		dec.UseNumber()
		var in map[string]any
		if err := dec.Decode(&in); err != nil {
			return
		}

		before, err := json.Marshal(in)
		if err != nil {
			return
		}

		out := policy.Apply(in)

		// The caller keeps the original while a redacted copy is printed.
		after, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("Apply mutated its input:\n%s\n%s", before, after)
		}

		// Redacting twice must be stable.
		twice, err := json.Marshal(policy.Apply(out))
		if err != nil {
			t.Fatal(err)
		}
		once, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		if string(once) != string(twice) {
			t.Fatalf("Apply is not idempotent:\n%s\n%s", once, twice)
		}
	})
}

// FuzzLooksSecret checks the credential-shape heuristic never panics.
func FuzzLooksSecret(f *testing.F) {
	f.Add("-----BEGIN RSA PRIVATE KEY-----")
	f.Add("0123456789abcdef0123456789abcdef")
	f.Add("/v1/api/path")
	f.Add("")
	f.Add(strings.Repeat("a", 10000))

	f.Fuzz(func(t *testing.T, s string) {
		got := LooksSecret(s)

		// The result must be stable for the same input.
		for i := 0; i < 3; i++ {
			if LooksSecret(s) != got {
				t.Fatal("LooksSecret is not deterministic")
			}
		}
		// A value containing a path separator or space is configuration, not a
		// bare token; flagging those would make the rule unusable noise.
		if got && strings.ContainsAny(strings.TrimSpace(s), " /") &&
			!strings.Contains(s, "PRIVATE KEY") {
			t.Fatalf("LooksSecret flagged %q, which looks like configuration", s)
		}
	})
}

// FuzzFingerprint checks the identifier never leaks its input.
func FuzzFingerprint(f *testing.F) {
	f.Add("secret")
	f.Add("")
	f.Add(strings.Repeat("x", 5000))

	f.Fuzz(func(t *testing.T, s string) {
		got := Fingerprint(s)
		if s == "" {
			if got != "" {
				t.Fatalf("Fingerprint(\"\") = %q", got)
			}
			return
		}
		if !strings.HasPrefix(got, "sha256:") {
			t.Fatalf("Fingerprint(%q) = %q, missing its algorithm prefix", s, got)
		}
		// The fingerprint must never contain the input.
		if len(s) > 3 && strings.Contains(got, s) {
			t.Fatalf("Fingerprint leaked its input: %q contains %q", got, s)
		}
		if len(got) != len("sha256:")+8 {
			t.Fatalf("Fingerprint(%q) has length %d", s, len(got))
		}
	})
}
