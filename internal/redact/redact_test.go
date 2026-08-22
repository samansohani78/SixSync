package redact

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func testPolicy() *Policy {
	return NewPolicy(
		[]string{
			"ssl.key",
			"plugins.*.password",
			"plugins.key-auth.key",
			"nodes.credential",
		},
		[]string{"authorization", "secret_key"},
	)
}

func TestApplyRedactsByPath(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "exact path is redacted",
			in:   map[string]any{"ssl": map[string]any{"key": "PRIVKEY", "cert": "CERT"}},
			want: map[string]any{"ssl": map[string]any{"key": Placeholder, "cert": "CERT"}},
		},
		{
			name: "wildcard segment matches one level",
			in: map[string]any{"plugins": map[string]any{
				"basic-auth": map[string]any{"password": "hunter2", "username": "bob"},
			}},
			want: map[string]any{"plugins": map[string]any{
				"basic-auth": map[string]any{"password": Placeholder, "username": "bob"},
			}},
		},
		{
			name: "name rule applies at any depth",
			in:   map[string]any{"a": map[string]any{"b": map[string]any{"authorization": "Bearer x"}}},
			want: map[string]any{"a": map[string]any{"b": map[string]any{"authorization": Placeholder}}},
		},
		{
			name: "array indices do not consume a path segment",
			in:   map[string]any{"nodes": []any{map[string]any{"credential": "s3cr3t", "host": "h1"}}},
			want: map[string]any{"nodes": []any{map[string]any{"credential": Placeholder, "host": "h1"}}},
		},
		{
			name: "unmatched fields are untouched",
			in:   map[string]any{"uri": "/x", "priority": 10, "enabled": true},
			want: map[string]any{"uri": "/x", "priority": 10, "enabled": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := testPolicy().Apply(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Apply()\n got = %#v\nwant = %#v", got, tt.want)
			}
		})
	}
}

// This is the case a name-based redactor gets wrong. upstream.key selects the
// consistent-hash input; redacting it would corrupt legitimate configuration
// output while adding no security.
func TestApplyDoesNotRedactNonSecretKeyFields(t *testing.T) {
	in := map[string]any{
		"upstream": map[string]any{"type": "chash", "hash_on": "vars", "key": "remote_addr"},
	}
	got := testPolicy().Apply(in).(map[string]any)
	up := got["upstream"].(map[string]any)
	if up["key"] != "remote_addr" {
		t.Errorf("upstream.key = %v, want it left intact (it is a hash input, not a secret)", up["key"])
	}
}

// Redaction must not damage the caller's data: drift holds the real value
// while a redacted copy is printed.
func TestApplyDoesNotMutateInput(t *testing.T) {
	in := map[string]any{
		"ssl":   map[string]any{"key": "PRIVKEY"},
		"nodes": []any{map[string]any{"credential": "s3cr3t"}},
	}
	before, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	_ = testPolicy().Apply(in)

	after, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("input was mutated:\nbefore = %s\nafter  = %s", before, after)
	}
}

// Sibling branches must not corrupt each other's path state.
func TestApplyHandlesSiblingsIndependently(t *testing.T) {
	in := map[string]any{
		"plugins": map[string]any{
			"a": map[string]any{"password": "p1", "keep": "1"},
			"b": map[string]any{"password": "p2", "keep": "2"},
			"c": map[string]any{"other": "not-secret"},
		},
	}
	got := testPolicy().Apply(in).(map[string]any)["plugins"].(map[string]any)

	for _, k := range []string{"a", "b"} {
		m := got[k].(map[string]any)
		if m["password"] != Placeholder {
			t.Errorf("plugins.%s.password = %v, want redacted", k, m["password"])
		}
		if m["keep"] == Placeholder {
			t.Errorf("plugins.%s.keep was redacted; only password should be", k)
		}
	}
	if got["c"].(map[string]any)["other"] != "not-secret" {
		t.Error("plugins.c.other was redacted; no rule matches it")
	}
}

func TestZeroPolicyRedactsNothing(t *testing.T) {
	var p *Policy
	in := map[string]any{"password": "hunter2"}
	got := p.Apply(in).(map[string]any)
	if got["password"] != "hunter2" {
		t.Error("a nil Policy redacted a value; it must be an explicit no-op")
	}
}

func TestValue(t *testing.T) {
	if got := Value("supersecret"); got != Placeholder {
		t.Errorf("Value() = %q, want %q", got, Placeholder)
	}
	// An unset value must stay visibly unset rather than look configured.
	if got := Value(""); got != "" {
		t.Errorf("Value(\"\") = %q, want empty", got)
	}
}

func TestPlaceholderLeaksNothingAboutTheOriginal(t *testing.T) {
	short := Value("a")
	long := Value(strings.Repeat("a", 4096))
	if short != long {
		t.Error("placeholder varies with input; it must not leak length information")
	}
}

func TestFingerprint(t *testing.T) {
	a := Fingerprint("key-one")
	b := Fingerprint("key-two")

	if a == b {
		t.Error("different secrets produced the same fingerprint")
	}
	if a != Fingerprint("key-one") {
		t.Error("fingerprint is not deterministic")
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Errorf("fingerprint %q lacks its algorithm prefix", a)
	}
	if len(a) != len("sha256:")+8 {
		t.Errorf("fingerprint %q has unexpected length %d", a, len(a))
	}
	if strings.Contains(a, "key-one") {
		t.Error("fingerprint contains the plaintext secret")
	}
	if got := Fingerprint(""); got != "" {
		t.Errorf("Fingerprint(\"\") = %q, want empty", got)
	}
}

func TestLooksSecret(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"pem private key armour", "-----BEGIN RSA PRIVATE KEY-----\n<not a real key>", true},
		{"pem ec private key", "-----BEGIN EC PRIVATE KEY-----", true},
		{"32 hex characters, the shape of an apisix admin key", "0123456789abcdef0123456789abcdef", true},
		{"long base64 token", "aGVsbG93b3JsZGhlbGxvd29ybGRoZWxsb3dvcmxk", true},
		{"short word", "password", false},
		{"uri path", "/v1/api/report/export", false},
		{"hostname", "gateway.internal.example.com", false},
		{"url", "https://example.com/aVeryLongOpaqueLookingPathSegment123456", false},
		{"public certificate armour is not a private key", "-----BEGIN CERTIFICATE-----", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksSecret(tt.in); got != tt.want {
				t.Errorf("LooksSecret(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// MatchesPath must agree with Apply, or a diff renderer consulting it would
// redact a different set of fields than the value redactor does.
func TestMatchesPathAgreesWithApply(t *testing.T) {
	p := testPolicy()

	cases := []struct {
		path []string
		want bool
	}{
		{[]string{"ssl", "key"}, true},
		{[]string{"plugins", "basic-auth", "password"}, true},
		{[]string{"plugins", "key-auth", "key"}, true},
		{[]string{"a", "b", "authorization"}, true},
		{[]string{"upstream", "key"}, false},
		{[]string{"uri"}, false},
		{[]string{"ssl", "cert"}, false},
	}
	for _, tc := range cases {
		if got := p.MatchesPath(tc.path); got != tc.want {
			t.Errorf("MatchesPath(%v) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestMatchesPathOnNilPolicy(t *testing.T) {
	var p *Policy
	if p.MatchesPath([]string{"password"}) {
		t.Error("a nil policy must match nothing")
	}
}
