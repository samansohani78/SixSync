package model

import (
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/redact"
)

func TestSupportedKindsAreExactlyTheDocumentedSet(t *testing.T) {
	// v0.1 manages these and only these. Adding a kind is a deliberate act
	// that must come with import, canonicalization and test coverage, so it
	// should also require editing this list.
	want := map[Kind]bool{
		KindRoute: true, KindService: true, KindUpstream: true,
		KindPluginConfig: true, KindGlobalRule: true,
	}
	for _, d := range All() {
		if d.Supported != want[d.Kind] {
			t.Errorf("kind %s: Supported = %v, want %v", d.Kind, d.Supported, want[d.Kind])
		}
	}
}

// An unsupported kind must be able to explain itself. A refusal with no reason
// is indistinguishable from a bug, from the user's side.
func TestUnsupportedKindsExplainThemselves(t *testing.T) {
	for _, d := range All() {
		if d.Supported {
			if d.RefusalReason != "" {
				t.Errorf("kind %s is supported but carries a refusal reason", d.Kind)
			}
			continue
		}
		if strings.TrimSpace(d.RefusalReason) == "" {
			t.Errorf("kind %s is unsupported but gives no reason", d.Kind)
		}
	}
}

func TestEveryKindHasPathAndIdentity(t *testing.T) {
	for _, d := range All() {
		if d.APIPath == "" {
			t.Errorf("kind %s has no APIPath", d.Kind)
		}
		if d.IdentityField == "" {
			t.Errorf("kind %s has no IdentityField", d.Kind)
		}
	}
}

// Not every kind is keyed by "id"; assuming so is a bug the registry exists to
// prevent.
func TestNonIdIdentityFields(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{KindConsumer, "username"},
		{KindPluginMetadata, "name"},
		{KindRoute, "id"},
	}
	for _, tt := range tests {
		d, ok := Lookup(tt.kind)
		if !ok {
			t.Fatalf("kind %s is not registered", tt.kind)
		}
		if d.IdentityField != tt.want {
			t.Errorf("%s identity field = %q, want %q", tt.kind, d.IdentityField, tt.want)
		}
	}
}

func TestAPIPathsAreUniqueAndResolvable(t *testing.T) {
	seen := map[string]Kind{}
	for _, d := range All() {
		if prev, dup := seen[d.APIPath]; dup {
			t.Errorf("APIPath %q is used by both %s and %s", d.APIPath, prev, d.Kind)
		}
		seen[d.APIPath] = d.Kind

		got, ok := ByAPIPath(d.APIPath)
		if !ok || got.Kind != d.Kind {
			t.Errorf("ByAPIPath(%q) did not round-trip to %s", d.APIPath, d.Kind)
		}
	}
}

// Ordering feeds directly into command output, which must be reproducible.
func TestAllIsDeterministicallyOrdered(t *testing.T) {
	first := All()
	for i := 0; i < 20; i++ {
		next := All()
		for j := range first {
			if first[j].Kind != next[j].Kind {
				t.Fatalf("All() ordering varies between calls at index %d", j)
			}
		}
	}
}

func TestReferencesPointAtRegisteredKinds(t *testing.T) {
	for _, d := range All() {
		for _, ref := range d.References {
			if _, ok := Lookup(ref.Target); !ok {
				t.Errorf("%s.%s references unregistered kind %s", d.Kind, ref.Field, ref.Target)
			}
			if ref.Field == "" {
				t.Errorf("%s has a reference with no field name", d.Kind)
			}
		}
	}
}

func TestRouteReferencesMatchAPISIX(t *testing.T) {
	d, _ := Lookup(KindRoute)
	want := map[string]Kind{
		"upstream_id":      KindUpstream,
		"service_id":       KindService,
		"plugin_config_id": KindPluginConfig,
	}
	if len(d.References) != len(want) {
		t.Fatalf("Route has %d references, want %d", len(d.References), len(want))
	}
	for _, ref := range d.References {
		if want[ref.Field] != ref.Target {
			t.Errorf("Route.%s targets %s, want %s", ref.Field, ref.Target, want[ref.Field])
		}
	}
}

func TestRedactionPolicyProtectsPluginCredentials(t *testing.T) {
	spec := map[string]any{
		"uri": "/pay",
		"plugins": map[string]any{
			"key-auth":   map[string]any{"key": "live-api-key"},
			"basic-auth": map[string]any{"username": "svc", "password": "hunter2"},
		},
	}
	got := RedactionPolicy(KindRoute).Apply(spec).(map[string]any)
	plugins := got["plugins"].(map[string]any)

	if plugins["key-auth"].(map[string]any)["key"] != redact.Placeholder {
		t.Error("plugins.key-auth.key was not redacted")
	}
	if plugins["basic-auth"].(map[string]any)["password"] != redact.Placeholder {
		t.Error("plugins.basic-auth.password was not redacted")
	}
	if plugins["basic-auth"].(map[string]any)["username"] == redact.Placeholder {
		t.Error("plugins.basic-auth.username was redacted; a username is not a credential")
	}
	if got["uri"] != "/pay" {
		t.Error("uri was redacted")
	}
}

// The precise case a name-based policy gets wrong.
func TestUpstreamHashKeyIsNotTreatedAsSecret(t *testing.T) {
	spec := map[string]any{"type": "chash", "hash_on": "vars", "key": "remote_addr"}
	got := RedactionPolicy(KindUpstream).Apply(spec).(map[string]any)
	if got["key"] != "remote_addr" {
		t.Errorf("upstream.key = %v, want it preserved (it is a hash input)", got["key"])
	}
}

func TestSSLPrivateKeyIsRedacted(t *testing.T) {
	spec := map[string]any{"cert": "PUBLIC-CERT", "key": "PRIVATE-KEY", "snis": []any{"a.example.test"}}
	got := RedactionPolicy(KindSSL).Apply(spec).(map[string]any)
	if got["key"] != redact.Placeholder {
		t.Error("ssl.key was not redacted")
	}
	if got["cert"] != "PUBLIC-CERT" {
		t.Error("ssl.cert was redacted; a certificate is public")
	}
}

// Failing open on redaction would be the worst possible default.
func TestRedactionPolicyForUnknownKindStillRedacts(t *testing.T) {
	got := RedactionPolicy(Kind("NoSuchKind")).Apply(
		map[string]any{"password": "hunter2", "uri": "/x"},
	).(map[string]any)
	if got["password"] != redact.Placeholder {
		t.Error("unknown kind produced a policy that leaks passwords")
	}
}
