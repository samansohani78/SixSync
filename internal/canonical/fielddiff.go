package canonical

import (
	"sort"
	"strconv"
	"strings"
)

// ChangeType is what happened to one field.
type ChangeType string

const (
	// ChangeAdded means the field exists in the desired state only.
	ChangeAdded ChangeType = "added"
	// ChangeRemoved means the field exists in the observed state only.
	ChangeRemoved ChangeType = "removed"
	// ChangeModified means the field exists on both sides with different values.
	ChangeModified ChangeType = "modified"
)

// FieldChange is one difference between two canonicalized resources.
//
// Old and New are rendered strings rather than raw values, because a change is
// something a human reads in a review. They must be passed through redaction
// before display or serialization — see Redact.
type FieldChange struct {
	Path string     `json:"path"`
	Type ChangeType `json:"type"`
	Old  string     `json:"old,omitempty"`
	New  string     `json:"new,omitempty"`
}

// DiffFields reports the differences between two canonicalized resource bodies,
// as a flat list of dotted paths ordered deterministically.
//
// Both inputs must already be canonical. Diffing raw Admin API bodies would
// report field ordering, node encoding and server timestamps as changes, which
// is exactly the noise canonicalization exists to remove.
func DiffFields(observed, desired map[string]any) []FieldChange {
	var out []FieldChange
	diffValue(nil, observed, desired, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func diffValue(path []string, observed, desired any, out *[]FieldChange) {
	oMap, oIsMap := observed.(map[string]any)
	dMap, dIsMap := desired.(map[string]any)

	if oIsMap && dIsMap {
		keys := map[string]struct{}{}
		for k := range oMap {
			keys[k] = struct{}{}
		}
		for k := range dMap {
			keys[k] = struct{}{}
		}

		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)

		for _, k := range sorted {
			ov, oHas := oMap[k]
			dv, dHas := dMap[k]
			child := append(append([]string{}, path...), k)

			switch {
			case oHas && !dHas:
				emitLeaves(child, ov, ChangeRemoved, out)
			case !oHas && dHas:
				emitLeaves(child, dv, ChangeAdded, out)
			default:
				diffValue(child, ov, dv, out)
			}
		}
		return
	}

	oList, oIsList := observed.([]any)
	dList, dIsList := desired.([]any)

	if oIsList && dIsList {
		max := len(oList)
		if len(dList) > max {
			max = len(dList)
		}
		for i := 0; i < max; i++ {
			child := append(append([]string{}, path...), "["+strconv.Itoa(i)+"]")
			switch {
			case i >= len(dList):
				emitLeaves(child, oList[i], ChangeRemoved, out)
			case i >= len(oList):
				emitLeaves(child, dList[i], ChangeAdded, out)
			default:
				diffValue(child, oList[i], dList[i], out)
			}
		}
		return
	}

	if render(observed) != render(desired) {
		*out = append(*out, FieldChange{
			Path: joinPath(path), Type: ChangeModified,
			Old: render(observed), New: render(desired),
		})
	}
}

// emitLeaves records an added or removed value as one change per leaf field.
//
// Rendering a whole subtree as a single value would defeat path-based
// redaction: a credential nested inside an added `plugins` block would be
// serialized into the change for `plugins`, a path no secret rule matches, and
// would then appear verbatim in the plan artifact and in CI logs. Descending to
// leaves gives every value a path the redactor can recognise.
//
// It also makes the diff more readable, which is the same reason a reviewer
// wants it.
func emitLeaves(path []string, v any, kind ChangeType, out *[]FieldChange) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			// An empty map is meaningful — `plugins: {cors: {}}` enables a
			// plugin with no configuration — so record it rather than dropping it.
			appendLeaf(path, "{}", kind, out)
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			emitLeaves(append(append([]string{}, path...), k), t[k], kind, out)
		}

	case []any:
		if len(t) == 0 {
			appendLeaf(path, "[]", kind, out)
			return
		}
		for i, item := range t {
			emitLeaves(append(append([]string{}, path...), "["+strconv.Itoa(i)+"]"),
				item, kind, out)
		}

	default:
		appendLeaf(path, render(v), kind, out)
	}
}

func appendLeaf(path []string, rendered string, kind ChangeType, out *[]FieldChange) {
	c := FieldChange{Path: joinPath(path), Type: kind}
	if kind == ChangeRemoved {
		c.Old = rendered
	} else {
		c.New = rendered
	}
	*out = append(*out, c)
}

// joinPath renders a path, keeping list indices attached to their parent rather
// than separated by a dot: "nodes[0].host", not "nodes.[0].host".
func joinPath(path []string) string {
	var b strings.Builder
	for i, seg := range path {
		if i > 0 && !strings.HasPrefix(seg, "[") {
			b.WriteByte('.')
		}
		b.WriteString(seg)
	}
	return b.String()
}

// render produces a stable string for a value. Composite values go through
// canonical JSON so that two equal structures always render identically.
func render(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, err := Marshal(v)
		if err != nil {
			return "<unrenderable>"
		}
		return string(b)
	}
}

// Redactor replaces a value at a path when it is sensitive.
type Redactor interface {
	// RedactPath reports whether a dotted path holds sensitive data, and what
	// to substitute for it.
	RedactPath(path string) (string, bool)
}

// Redact returns a copy of the changes with sensitive values replaced.
//
// A diff is written into plan artifacts, CI logs and review comments, so a
// credential appearing in one has escaped to all three at once. The path is
// kept — knowing *that* a secret changed is useful — while the values are not.
func Redact(changes []FieldChange, r Redactor) []FieldChange {
	if r == nil {
		return changes
	}
	out := make([]FieldChange, len(changes))
	for i, c := range changes {
		out[i] = c
		if placeholder, sensitive := r.RedactPath(c.Path); sensitive {
			if out[i].Old != "" {
				out[i].Old = placeholder
			}
			if out[i].New != "" {
				out[i].New = placeholder
			}
		}
	}
	return out
}
