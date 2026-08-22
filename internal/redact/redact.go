// Package redact removes sensitive values from data before it reaches a log,
// a terminal, a plan artifact, or a file on disk.
//
// Redaction here is deliberately path-aware rather than name-aware. A blanket
// rule on the field name "key" would be both wrong and useless for APISIX:
// upstream.key is a consistent-hash input (not a secret), while ssl.key and
// plugins.key-auth.key are credentials. Matching on position lets us redact
// the credentials without drowning ordinary configuration in placeholders.
//
// The package is deliberately free of APISIX knowledge. Callers supply a
// Policy; internal/model owns the APISIX-specific one. That keeps this package
// independently testable and prevents an import cycle.
package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Placeholder replaces every redacted value. It is intentionally not
// value-shaped: no length hint, no prefix, no suffix. Anything derived from
// the original would leak information about it.
const Placeholder = "[REDACTED]"

// Policy decides which fields are sensitive.
//
// The zero Policy redacts nothing, which makes "no policy" a visible mistake
// in tests rather than a silent one in production.
type Policy struct {
	// paths are dot-separated patterns matched against a field's position in
	// the document. A "*" segment matches exactly one path segment.
	// Example: "plugins.*.password" matches plugins.basic-auth.password.
	paths []pathPattern

	// names are field names that are sensitive wherever they appear. Reserve
	// this for names that are unambiguous in every context, such as
	// "authorization". Compared case-insensitively.
	names map[string]struct{}
}

type pathPattern []string

// NewPolicy builds a Policy from dot-separated path patterns and
// location-independent field names.
func NewPolicy(paths []string, names []string) *Policy {
	p := &Policy{names: make(map[string]struct{}, len(names))}
	for _, raw := range paths {
		if raw == "" {
			continue
		}
		p.paths = append(p.paths, strings.Split(raw, "."))
	}
	for _, n := range names {
		p.names[strings.ToLower(n)] = struct{}{}
	}
	return p
}

// matches reports whether a field at the given path is sensitive.
func (p *Policy) matches(path []string) bool {
	if p == nil || len(path) == 0 {
		return false
	}
	if _, ok := p.names[strings.ToLower(path[len(path)-1])]; ok {
		return true
	}
	for _, pat := range p.paths {
		if pat.match(path) {
			return true
		}
	}
	return false
}

func (pat pathPattern) match(path []string) bool {
	if len(pat) != len(path) {
		return false
	}
	for i, seg := range pat {
		if seg == "*" {
			continue
		}
		if !strings.EqualFold(seg, path[i]) {
			return false
		}
	}
	return true
}

// Apply returns a redacted deep copy of v. The input is never modified:
// callers such as drift and import rely on holding the unredacted value while
// a redacted one is being printed.
//
// Array indices do not consume a path segment. A pattern like "nodes.password"
// therefore matches nodes[0].password, which is what a reader of the pattern
// expects.
func (p *Policy) Apply(v any) any {
	return p.walk(v, nil)
}

func (p *Policy) walk(v any, path []string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			// A fresh slice per child. Reusing path's backing array here
			// would let sibling branches overwrite each other's segments.
			child := make([]string, len(path)+1)
			copy(child, path)
			child[len(path)] = k

			if p.matches(child) {
				out[k] = Placeholder
				continue
			}
			out[k] = p.walk(val, child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = p.walk(val, path)
		}
		return out
	default:
		return v
	}
}

// Value redacts a single sensitive scalar, such as an API key held in
// configuration. An empty string stays empty: reporting "[REDACTED]" for a
// value that was never set would be misleading.
func Value(s string) string {
	if s == "" {
		return ""
	}
	return Placeholder
}

// Fingerprint identifies a secret without disclosing it, so that an operator
// can confirm *which* credential is in use — for example when a pipeline has
// several. It is a truncated SHA-256 of the value.
//
// 8 hex characters (32 bits) is enough to distinguish a handful of keys an
// operator might confuse, while being far too short to attack the preimage of
// a high-entropy secret. Never use this to compare against an expected value.
func Fingerprint(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])[:8]
}

// pemBlock matches any PEM armour, which is the highest-confidence signal that
// a repository contains key material.
var pemBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)

// longOpaque matches a long unbroken run of base64/hex-alphabet characters:
// the shape of an API key, a token, or a signature.
//
// The 32-character floor is a deliberate trade-off. Real APISIX admin keys are
// 32 hex characters, so a lower bound would be needed to catch them; but going
// lower would flag ordinary identifiers and hostnames. Callers should treat a
// positive result as "worth a human look", never as proof.
var longOpaque = regexp.MustCompile(`^[A-Za-z0-9+/_-]{32,}={0,2}$`)

// LooksSecret reports whether a string has the shape of credential material.
//
// This is a heuristic used to warn about plaintext secrets in a repository. It
// is a backstop for human error, not a security control: it has both false
// positives (a long opaque identifier) and false negatives (a short password).
// Never rely on it to decide whether something is safe to publish.
func LooksSecret(s string) bool {
	if pemBlock.MatchString(s) {
		return true
	}
	t := strings.TrimSpace(s)
	// A path or URL that merely happens to be long is not a secret.
	if strings.ContainsAny(t, "/:. ") {
		return false
	}
	return longOpaque.MatchString(t)
}
