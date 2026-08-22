package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// toCanonicalValue converts a value to the decoded-JSON form the canonical
// encoder understands, preserving numeric precision.
//
// Going through JSON rather than reflecting over Go structures means the digest
// is defined by the serialized form a reviewer can actually read.
func toCanonicalValue(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Write serializes a plan as indented JSON.
//
// Indented on purpose: a plan is reviewed by humans in pull requests and CI
// logs, and a single-line artifact is not reviewable. Indentation is applied to
// the *output*, never to the digest, which is computed over canonical form.
func Write(w io.Writer, p *Plan) error {
	if p.Digest == "" {
		return fmt.Errorf("refusing to write an unsealed plan: call Seal first")
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(p)
}

// Read parses a plan and verifies its integrity.
//
// Unknown fields are rejected. A plan is a contract between two commands, so a
// field this build does not understand means the artifact was produced by a
// different version or was tampered with; either way, acting on it would be
// acting on something not fully understood.
func Read(r io.Reader) (*Plan, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	dec.UseNumber()

	var p Plan
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}
	// Exactly one JSON document, so trailing content cannot smuggle anything in.
	if dec.More() {
		return nil, fmt.Errorf("plan contains trailing content after the first document")
	}

	if p.Content.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("plan format %q is not supported by this build, which implements %q",
			p.Content.FormatVersion, FormatVersion)
	}
	if err := p.Verify(); err != nil {
		return nil, err
	}
	return &p, nil
}
