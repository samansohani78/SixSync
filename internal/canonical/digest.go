package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Digest returns a stable SHA-256 over a canonicalized value.
//
// Two resources with the same meaning produce the same digest regardless of
// how their JSON happened to be ordered or spaced. This is what `drift` uses
// to decide whether a resource changed, and what a future `apply` will record
// so it can detect that remote state moved after a plan was built.
func Digest(v any) (string, error) {
	var buf bytes.Buffer
	if err := writeCanonicalJSON(&buf, v); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Marshal renders a value as canonical JSON: object keys sorted, no
// insignificant whitespace, no HTML escaping.
//
// encoding/json already sorts map keys, but it is not used directly here
// because it also escapes <, > and & into <-style sequences. That is
// harmless for correctness but makes golden files unreadable, and readable
// golden files are the point of having them.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonicalJSON(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonicalJSON(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSONString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonicalJSON(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil

	case []any:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalJSON(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil

	case json.Number:
		// Written verbatim, preserving both precision and the
		// integer-versus-float distinction.
		buf.WriteString(t.String())
		return nil

	case string:
		return writeJSONString(buf, t)

	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil

	case nil:
		buf.WriteString("null")
		return nil

	default:
		return fmt.Errorf("cannot serialize value of type %T", v)
	}
}

func writeJSONString(buf *bytes.Buffer, s string) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	// Encode appends a newline that has no place inside a JSON document.
	buf.Truncate(buf.Len() - 1)
	return nil
}
