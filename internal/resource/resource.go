// Package resource defines SixSync's on-disk representation of an APISIX
// resource, and reads and writes it deterministically.
//
// The format wraps the APISIX resource body rather than replacing it. `spec` is
// exactly what the Admin API accepts, so an operator who knows APISIX can read
// a SixSync file without learning a translation layer, and SixSync never has to
// model APISIX's schema in order to round-trip a resource it does not fully
// understand.
//
// The wrapper carries only what APISIX cannot: which kind this is, its stable
// identity, and whether the operator intends it to exist.
package resource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/samansohani78/SixSync/internal/model"
)

// APIVersion is the current on-disk format version.
//
// The group is deliberately not a domain name. A group such as
// "gitops.apisix.dev" would assert ownership of a domain built on an Apache
// trademark, which the ASF trademark policy prohibits; and SixSync should not
// claim a domain it does not control either.
const APIVersion = "sixsync/v1alpha1"

// State is the operator's intent for a resource.
type State string

const (
	// StatePresent means the resource should exist as described.
	StatePresent State = "present"

	// StateAbsent means the operator has explicitly asked for the resource to
	// be deleted.
	//
	// Deletion is never inferred from a file being missing. A deleted file is
	// far more likely to be a bad merge, a partial checkout, or a mistaken
	// path filter than a genuine intent to remove a live route. Requiring the
	// intent to be written down means it appears in a diff and gets reviewed.
	StateAbsent State = "absent"
)

// Metadata carries identity and human context.
type Metadata struct {
	// ID is the APISIX identity, preserved exactly. Whatever field the kind
	// uses for identity — id, username, plugin name — is stored here.
	//
	// Preserving it is what allows SixSync to adopt an existing gateway
	// without recreating resources and dropping traffic.
	ID string `yaml:"id"`

	// Name is a human label. For most kinds it is also APISIX's own optional
	// `name` field, mirrored here so that file names and diffs are readable.
	Name string `yaml:"name,omitempty"`

	// Annotations are free-form operator notes. SixSync never sends them to
	// APISIX.
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

// Lifecycle expresses intent.
type Lifecycle struct {
	State State `yaml:"state"`

	// Reason documents why a resource is marked absent, so that a deletion in
	// a diff carries its own justification.
	Reason string `yaml:"reason,omitempty"`
}

// File is one SixSync resource file.
//
// Field order here is the field order on disk, which keeps written files
// readable and diffs minimal.
type File struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       model.Kind `yaml:"kind"`
	Metadata   Metadata   `yaml:"metadata"`
	Lifecycle  Lifecycle  `yaml:"lifecycle"`

	// Spec is the APISIX resource body, verbatim.
	//
	// Unknown fields inside spec are preserved rather than rejected: APISIX
	// gains fields between releases, and a tool that silently dropped them
	// would corrupt configuration it did not understand. Unknown fields at the
	// *top* level are rejected, because those would be SixSync's own concern
	// and an unrecognized one means a typo or a format mismatch.
	Spec map[string]any `yaml:"spec"`
}

// idPattern is APISIX's own identifier rule, from schema_def.lua at 3.18.0:
// a string of 1-64 characters drawn from letters, digits, dash, underscore
// and dot.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

// ValidateID reports whether an identifier is one APISIX will accept.
//
// Checking this locally means `lint` can reject an unusable ID during review
// rather than at apply time, when it would fail partway through a rollout.
func ValidateID(id string) error {
	if id == "" {
		return errors.New("identifier is empty")
	}
	if len(id) > 64 {
		return fmt.Errorf("identifier is %d characters; APISIX allows at most 64", len(id))
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("identifier %q contains characters APISIX rejects; "+
			"allowed are letters, digits, dot, dash and underscore", id)
	}

	// "." and ".." satisfy APISIX's character rule but name directory entries.
	// They are meaningless as identifiers and dangerous anywhere a path is
	// built, so they are refused outright rather than relied on being
	// neutralised downstream.
	if id == "." || id == ".." {
		return fmt.Errorf("identifier %q is a directory reference, not a usable identifier", id)
	}
	return nil
}

// Validate checks a decoded file's structure. It deliberately does not check
// the contents of spec: that is APISIX's schema to enforce, and duplicating it
// here would rot with every APISIX release.
func (f *File) Validate() error {
	if f.APIVersion != APIVersion {
		return fmt.Errorf("unsupported apiVersion %q; this build of SixSync understands %q",
			f.APIVersion, APIVersion)
	}

	def, ok := model.Lookup(f.Kind)
	if !ok {
		return fmt.Errorf("unknown kind %q", f.Kind)
	}
	if !def.Supported {
		return fmt.Errorf("kind %s is not supported: %s", f.Kind, def.RefusalReason)
	}

	if err := ValidateID(f.Metadata.ID); err != nil {
		return fmt.Errorf("metadata.id: %w", err)
	}

	switch f.Lifecycle.State {
	case StatePresent:
		if len(f.Spec) == 0 {
			return errors.New("spec is empty; a resource that should exist must describe itself")
		}
	case StateAbsent:
		// A tombstone needs no spec: the resource is identified by kind and ID.
	case "":
		return errors.New("lifecycle.state is required; set it to \"present\" or \"absent\"")
	default:
		return fmt.Errorf("lifecycle.state %q is not valid; use \"present\" or \"absent\"", f.Lifecycle.State)
	}

	// APISIX rejects a write carrying these, so a file containing them could
	// never be applied.
	for _, forbidden := range []string{"create_time", "update_time"} {
		if _, present := f.Spec[forbidden]; present {
			return fmt.Errorf("spec.%s is set by APISIX and rejected on write; remove it", forbidden)
		}
	}

	// Identity belongs in metadata, and having it in both places invites them
	// to disagree.
	if _, present := f.Spec[def.IdentityField]; present {
		return fmt.Errorf("spec.%s duplicates metadata.id; identity belongs only in metadata",
			def.IdentityField)
	}

	return nil
}

// Decode parses a resource file strictly.
//
// Unknown top-level fields are an error: they are SixSync's own vocabulary, so
// an unrecognized one means a typo or a file written for a different format
// version, and silently ignoring it would discard operator intent.
func Decode(data []byte) (*File, error) {
	var f File
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("parsing resource file: %w", err)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// Encode renders a resource file deterministically.
//
// The same input must always produce byte-identical output, or `import` would
// generate spurious diffs on re-runs and the golden tests would be worthless.
func Encode(f *File) ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}

	// Numbers arrive from the Admin API as json.Number, which is a string
	// type. Handed to a YAML encoder as-is it would be quoted, and a port
	// written as "9090" is rejected by APISIX's schema. Convert them to a form
	// that emits bare digits, preserving the exact text so that precision and
	// the integer/float distinction both survive.
	encodable := *f
	encodable.Spec = numbersForYAML(f.Spec).(map[string]any)

	// goccy/go-yaml emits map keys in sorted order, so a spec decoded into a
	// map[string]any serializes identically every time. TestEncodeIsDeterministic
	// pins that behaviour, since determinism here is what makes import re-runs
	// and golden files meaningful.
	body, err := yaml.MarshalWithOptions(&encodable, yaml.Indent(2), yaml.UseLiteralStyleIfMultiline(true))
	if err != nil {
		return nil, fmt.Errorf("encoding resource file: %w", err)
	}

	header := "# Managed by SixSync. Edit this file, review the change, then apply it.\n"
	return append([]byte(header), body...), nil
}

// FileName derives a stable, readable file name for a resource.
//
// The name is derived from the human label where there is one and the ID
// otherwise, then always suffixed with the ID. Including the ID guarantees
// uniqueness even when two resources share a label, and means renaming a
// resource does not silently create a second file for the same object.
func FileName(f *File) string {
	base := slug(f.Metadata.Name)
	if base == "" {
		base = slug(f.Metadata.ID)
	}
	idPart := slug(f.Metadata.ID)

	// Every character can be stripped by slugging — an identifier of "..", or
	// a name of only punctuation. A stemless ".yaml" is a hidden file that the
	// loader then skips, so the resource would silently vanish. Fall back to a
	// stable derived name instead.
	if base == "" {
		base = "resource-" + shortHash(f.Metadata.ID)
	}

	if base == idPart || idPart == "" {
		return base + ".yaml"
	}
	return base + "-" + idPart + ".yaml"
}

// shortHash derives a stable token from an identifier that slugs to nothing.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// nonSlug matches every character not allowed in a generated file name.
var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug reduces a string to a lowercase, filesystem-safe token.
//
// The result is bounded so that a long APISIX `name` cannot produce a path that
// exceeds a filesystem's limit.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

// DirName returns the directory a kind's files live in, e.g. "routes".
func DirName(kind model.Kind) (string, error) {
	def, ok := model.Lookup(kind)
	if !ok {
		return "", fmt.Errorf("unknown kind %q", kind)
	}
	return def.APIPath, nil
}

// yamlNumber renders a numeric value as bare digits rather than a quoted
// string, preserving the original text exactly.
//
// Round-tripping through int64 or float64 instead would be lossy: float64
// cannot represent an integer above 2^53 exactly, and APISIX reference values
// can exceed that.
type yamlNumber string

// MarshalYAML writes the number verbatim.
func (n yamlNumber) MarshalYAML() ([]byte, error) { return []byte(n), nil }

// numbersForYAML deep-copies a decoded value, converting every json.Number so
// that the YAML encoder emits it unquoted.
func numbersForYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = numbersForYAML(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = numbersForYAML(val)
		}
		return out
	case json.Number:
		return yamlNumber(t)
	default:
		return v
	}
}
