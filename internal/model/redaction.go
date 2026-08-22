package model

import "github.com/samansohani78/SixSync/internal/redact"

// alwaysSensitive are field names that carry credentials wherever they appear.
//
// Kept deliberately short. Every entry must be a name with no legitimate
// non-secret meaning anywhere in an APISIX resource — otherwise it belongs in a
// kind's SecretPaths, where position disambiguates it. "key" is the cautionary
// example: it is a private key on an SSL object and a hash input on an
// upstream, so it can never live in this list.
var alwaysSensitive = []string{
	"password",
	"authorization",
	"secret_key",
	"access_key_secret",
	"client_secret",
	"private_key",
}

// RedactionPolicy returns the redaction policy for a kind, combining that
// kind's positional secret paths with the location-independent names above.
//
// An unknown kind still yields a working policy rather than a nil one: failing
// open on redaction would be the worst possible default.
func RedactionPolicy(k Kind) *redact.Policy {
	d, ok := Lookup(k)
	if !ok {
		return redact.NewPolicy(nil, alwaysSensitive)
	}
	return redact.NewPolicy(d.SecretPaths, alwaysSensitive)
}
