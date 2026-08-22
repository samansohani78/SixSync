// Package model is the single source of truth for what SixSync knows about
// each APISIX resource type.
//
// Every per-kind fact lives here: the Admin API path, which field carries
// identity, which HTTP verbs APISIX actually implements, which fields hold
// credentials, and which fields reference other resources. Other packages ask
// this registry rather than carrying their own copy, which is what keeps a
// "switch on kind" from appearing in the client, the importer, the linter and
// the canonicalizer independently.
//
// Facts here are drawn from apisix/admin/*.lua and apisix/schema_def.lua at tag
// 3.18.0, and confirmed against a running 3.18.0 instance where behaviour was
// observable. See docs/compatibility.md.
package model

import "sort"

// Kind is a SixSync resource kind. The string value is what appears in a
// resource file's `kind:` field, so it is part of the on-disk format.
type Kind string

const (
	KindRoute          Kind = "Route"
	KindService        Kind = "Service"
	KindUpstream       Kind = "Upstream"
	KindPluginConfig   Kind = "PluginConfig"
	KindGlobalRule     Kind = "GlobalRule"
	KindConsumer       Kind = "Consumer"
	KindCredential     Kind = "Credential"
	KindSSL            Kind = "SSL"
	KindSecret         Kind = "Secret"
	KindStreamRoute    Kind = "StreamRoute"
	KindConsumerGroup  Kind = "ConsumerGroup"
	KindPluginMetadata Kind = "PluginMetadata"
	KindProto          Kind = "Proto"
)

// Reference is a field on one kind that points at another kind.
type Reference struct {
	// Field is the key inside spec, e.g. "upstream_id".
	Field string
	// Target is the kind the value identifies.
	Target Kind
}

// Definition is everything SixSync knows about one resource kind.
type Definition struct {
	Kind Kind

	// APIPath is the Admin API path segment, e.g. "routes" in
	// /apisix/admin/routes. Plural, matching APISIX's own naming.
	APIPath string

	// IdentityField is the field inside the APISIX resource body that carries
	// its identity. This is "id" for most kinds but deliberately not all:
	// Consumers are keyed by username and PluginMetadata by plugin name.
	IdentityField string

	// SupportsPOST reports whether APISIX will generate an ID for this kind.
	// Several kinds reject POST outright (unsupported_methods in the Lua
	// source). SixSync never uses POST — it always writes to a chosen ID so
	// that identity is decided in Git — but recording this lets `doctor` and
	// `lint` explain why an ID is mandatory for some kinds.
	SupportsPOST bool

	// SupportsPATCH mirrors the same source. SixSync never issues PATCH: on
	// APISIX it merges maps rather than replacing them, which makes the
	// resulting state depend on prior state instead of on the file in Git.
	SupportsPATCH bool

	// HasDeleteChecker reports whether APISIX itself refuses to delete this
	// kind while another resource references it. Where it is false, SixSync's
	// own dependency analysis is the only protection a user has.
	HasDeleteChecker bool

	// References are the outbound reference fields of this kind.
	References []Reference

	// SecretPaths are dot-separated paths within spec that hold credential
	// material, in the syntax internal/redact understands. A "*" matches
	// exactly one path segment.
	SecretPaths []string

	// Supported reports whether SixSync manages this kind today. Unsupported
	// kinds are still registered, so that the tool can refuse them by name
	// with a reason rather than silently ignoring them.
	Supported bool

	// RefusalReason explains, in terms a user can act on, why an unsupported
	// kind is refused. Empty when Supported is true.
	RefusalReason string
}

// pluginSecretPaths are the credential fields that appear inside a plugins
// block. Any kind that can carry plugins inherits these.
//
// Scoped under "plugins.*" on purpose: a bare "key" rule would also match
// upstream.key, which selects the consistent-hash input and is not a secret.
var pluginSecretPaths = []string{
	"plugins.*.password",
	"plugins.*.secret",
	"plugins.*.secret_key",
	"plugins.*.access_key",
	"plugins.*.token",
	"plugins.*.client_secret",
	"plugins.*.private_key",
	"plugins.key-auth.key",
	"plugins.jwt-auth.key",
	"plugins.jwt-auth.secret",
	"plugins.hmac-auth.secret_key",
	"plugins.basic-auth.password",
}

func withPluginSecrets(extra ...string) []string {
	out := make([]string, 0, len(pluginSecretPaths)+len(extra))
	out = append(out, pluginSecretPaths...)
	out = append(out, extra...)
	return out
}

var registry = map[Kind]Definition{
	KindRoute: {
		Kind:             KindRoute,
		APIPath:          "routes",
		IdentityField:    "id",
		SupportsPOST:     true,
		SupportsPATCH:    true,
		HasDeleteChecker: false,
		References: []Reference{
			{Field: "upstream_id", Target: KindUpstream},
			{Field: "service_id", Target: KindService},
			{Field: "plugin_config_id", Target: KindPluginConfig},
		},
		SecretPaths: withPluginSecrets("upstream.tls.client_key"),
		Supported:   true,
	},
	KindService: {
		Kind:             KindService,
		APIPath:          "services",
		IdentityField:    "id",
		SupportsPOST:     true,
		SupportsPATCH:    true,
		HasDeleteChecker: true,
		References: []Reference{
			{Field: "upstream_id", Target: KindUpstream},
		},
		SecretPaths: withPluginSecrets("upstream.tls.client_key"),
		Supported:   true,
	},
	KindUpstream: {
		Kind:             KindUpstream,
		APIPath:          "upstreams",
		IdentityField:    "id",
		SupportsPOST:     true,
		SupportsPATCH:    true,
		HasDeleteChecker: true,
		// upstream.key is the consistent-hash input, not a credential.
		SecretPaths: []string{"tls.client_key"},
		Supported:   true,
	},
	KindPluginConfig: {
		Kind:             KindPluginConfig,
		APIPath:          "plugin_configs",
		IdentityField:    "id",
		SupportsPOST:     false,
		SupportsPATCH:    true,
		HasDeleteChecker: true,
		SecretPaths:      withPluginSecrets(),
		Supported:        true,
	},
	KindGlobalRule: {
		Kind:          KindGlobalRule,
		APIPath:       "global_rules",
		IdentityField: "id",
		SupportsPOST:  false,
		SupportsPATCH: true,
		// No delete_checker in the 3.18.0 source: APISIX will delete a global
		// rule regardless of what depends on its behaviour.
		HasDeleteChecker: false,
		SecretPaths:      withPluginSecrets(),
		Supported:        true,
	},

	// --- Registered but refused -------------------------------------------
	// These are named so that SixSync can decline them explicitly. Silently
	// skipping a resource type would let a user believe their gateway is fully
	// represented in Git when it is not.

	KindConsumer: {
		Kind:          KindConsumer,
		APIPath:       "consumers",
		IdentityField: "username",
		SupportsPOST:  false,
		SupportsPATCH: false,
		SecretPaths:   withPluginSecrets(),
		Supported:     false,
		RefusalReason: "consumers carry authentication credentials, and APISIX decrypts " +
			"encrypt_fields when serving a read, so importing them would write plaintext " +
			"secrets into Git",
	},
	KindCredential: {
		Kind:          KindCredential,
		APIPath:       "credentials",
		IdentityField: "id",
		SupportsPOST:  false,
		SupportsPATCH: false,
		SecretPaths:   withPluginSecrets(),
		Supported:     false,
		RefusalReason: "credentials are secret material by definition, and are nested under " +
			"a consumer rather than addressable on their own",
	},
	KindSSL: {
		Kind:             KindSSL,
		APIPath:          "ssls",
		IdentityField:    "id",
		SupportsPOST:     true,
		SupportsPATCH:    true,
		HasDeleteChecker: false,
		SecretPaths:      []string{"key", "keys"},
		Supported:        false,
		RefusalReason: "APISIX strips the private key on read, so an imported SSL object " +
			"could never be applied back without the key being supplied separately",
	},
	KindSecret: {
		Kind:          KindSecret,
		APIPath:       "secrets",
		IdentityField: "id",
		SupportsPOST:  false,
		SupportsPATCH: true,
		SecretPaths: []string{
			"token", "secret_access_key", "session_token", "auth_config.private_key",
		},
		Supported:     false,
		RefusalReason: "secret manager configurations hold the credentials used to reach the secret manager itself",
	},
	KindStreamRoute: {
		Kind:             KindStreamRoute,
		APIPath:          "stream_routes",
		IdentityField:    "id",
		SupportsPOST:     true,
		SupportsPATCH:    false,
		HasDeleteChecker: true,
		References: []Reference{
			{Field: "upstream_id", Target: KindUpstream},
			{Field: "service_id", Target: KindService},
		},
		SecretPaths:   withPluginSecrets(),
		Supported:     false,
		RefusalReason: "stream routes require APISIX to run in stream proxy mode, which is not yet covered by SixSync's compatibility tests",
	},
	KindConsumerGroup: {
		Kind:             KindConsumerGroup,
		APIPath:          "consumer_groups",
		IdentityField:    "id",
		SupportsPOST:     false,
		SupportsPATCH:    true,
		HasDeleteChecker: true,
		SecretPaths:      withPluginSecrets(),
		Supported:        false,
		RefusalReason:    "consumer groups are only meaningful alongside consumers, which are not yet supported",
	},
	KindPluginMetadata: {
		Kind:          KindPluginMetadata,
		APIPath:       "plugin_metadata",
		IdentityField: "name",
		SupportsPOST:  false,
		SupportsPATCH: false,
		SecretPaths:   []string{"*.password", "*.secret", "*.token"},
		Supported:     false,
		RefusalReason: "plugin metadata is keyed by plugin name and is global to the instance, " +
			"which needs an ownership model SixSync does not have yet",
	},
	KindProto: {
		Kind:             KindProto,
		APIPath:          "protos",
		IdentityField:    "id",
		SupportsPOST:     true,
		SupportsPATCH:    false,
		HasDeleteChecker: true,
		Supported:        false,
		RefusalReason:    "protobuf definitions are only used by grpc-transcode, which is out of scope for the first release",
	},
}

// Lookup returns the definition for a kind.
func Lookup(k Kind) (Definition, bool) {
	d, ok := registry[k]
	return d, ok
}

// All returns every registered kind, sorted, so that iteration order is
// deterministic. Several commands produce output ordered by kind and must not
// vary between runs.
func All() []Definition {
	out := make([]Definition, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// Supported returns only the kinds SixSync manages, sorted.
func Supported() []Definition {
	var out []Definition
	for _, d := range All() {
		if d.Supported {
			out = append(out, d)
		}
	}
	return out
}

// ByAPIPath resolves an Admin API path segment back to a kind, so that a
// response key such as "/apisix/routes/1" can be attributed.
func ByAPIPath(path string) (Definition, bool) {
	for _, d := range registry {
		if d.APIPath == path {
			return d, true
		}
	}
	return Definition{}, false
}
