package plan

import (
	"strings"
	"testing"
)

func samplePlan(t *testing.T) *Plan {
	t.Helper()
	p := &Plan{Content: Content{
		FormatVersion:       FormatVersion,
		Target:              Target{AdminURL: "http://gw.example.test:9180", APISIXVersion: "3.18.0"},
		DesiredFingerprint:  "sha256:desired",
		ObservedFingerprint: "sha256:observed",
		Operations: []Operation{{
			Order: 0, Type: OpUpdate, Status: StatusPlanned,
			Kind: "Route", ID: "r1", SourcePath: "routes/r1.yaml",
			Precondition: Precondition{ObservedModifiedIndex: 42, ObservedDigest: "sha256:x"},
		}},
	}}
	if err := p.Seal(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWriteReadRoundTrip(t *testing.T) {
	original := samplePlan(t)

	var sb strings.Builder
	if err := Write(&sb, original); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Digest != original.Digest {
		t.Errorf("digest changed across the round trip: %s -> %s", original.Digest, got.Digest)
	}
	if len(got.Content.Operations) != 1 || got.Content.Operations[0].ID != "r1" {
		t.Errorf("operations did not survive: %+v", got.Content.Operations)
	}
	if got.Content.Operations[0].Precondition.ObservedModifiedIndex != 42 {
		t.Error("the precondition revision did not survive the round trip")
	}
}

// An unsealed plan has no integrity guarantee, so writing one would produce an
// artifact apply must reject anyway.
func TestWriteRefusesUnsealedPlan(t *testing.T) {
	p := &Plan{Content: Content{FormatVersion: FormatVersion}}
	if err := Write(&strings.Builder{}, p); err == nil {
		t.Fatal("expected Write to refuse a plan with no digest")
	}
}

// The point of the digest: a plan edited after generation must not be usable.
func TestReadDetectsTamperedContent(t *testing.T) {
	p := samplePlan(t)
	var sb strings.Builder
	if err := Write(&sb, p); err != nil {
		t.Fatal(err)
	}

	tampered := strings.Replace(sb.String(), `"id": "r1"`, `"id": "r-attacker"`, 1)
	if tampered == sb.String() {
		t.Fatal("the test did not actually modify the plan")
	}

	_, err := Read(strings.NewReader(tampered))
	if err == nil {
		t.Fatal("a tampered plan was accepted")
	}
	if !strings.Contains(err.Error(), "modified after it was generated") {
		t.Errorf("the error should say what happened: %v", err)
	}
}

func TestReadDetectsTamperedDigest(t *testing.T) {
	p := samplePlan(t)
	var sb strings.Builder
	if err := Write(&sb, p); err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(sb.String(), p.Digest, "sha256:0000000000000000", 1)

	if _, err := Read(strings.NewReader(tampered)); err == nil {
		t.Fatal("a plan with a replaced digest was accepted")
	}
}

func TestReadRejectsUnknownFormatVersion(t *testing.T) {
	p := samplePlan(t)
	p.Content.FormatVersion = "sixsync.plan/v99"
	if err := p.Seal(); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := Write(&sb, p); err != nil {
		t.Fatal(err)
	}

	_, err := Read(strings.NewReader(sb.String()))
	if err == nil {
		t.Fatal("a plan in an unknown format was accepted")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// A field this build does not understand means the artifact came from a
// different version or was modified; acting on it would be acting on something
// not fully understood.
func TestReadRejectsUnknownFields(t *testing.T) {
	p := samplePlan(t)
	var sb strings.Builder
	if err := Write(&sb, p); err != nil {
		t.Fatal(err)
	}
	withExtra := strings.Replace(sb.String(),
		`"format_version"`, `"unexpected_field": true,
    "format_version"`, 1)

	if _, err := Read(strings.NewReader(withExtra)); err == nil {
		t.Fatal("a plan with an unknown field was accepted")
	}
}

func TestReadRejectsTrailingContent(t *testing.T) {
	p := samplePlan(t)
	var sb strings.Builder
	if err := Write(&sb, p); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(strings.NewReader(sb.String() + "\n{\"another\":\"document\"}")); err == nil {
		t.Fatal("trailing content after the plan was accepted")
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	for _, input := range []string{"", "not json", "[]", "null", "{"} {
		if _, err := Read(strings.NewReader(input)); err == nil {
			t.Errorf("Read accepted %q", input)
		}
	}
}

func TestVerifyRejectsMissingDigest(t *testing.T) {
	p := &Plan{Content: Content{FormatVersion: FormatVersion}}
	if err := p.Verify(); err == nil {
		t.Fatal("a plan with no digest must not verify")
	}
}

// A large integer in a precondition must survive serialization exactly.
func TestPreconditionRevisionSurvivesLargeValues(t *testing.T) {
	p := samplePlan(t)
	const big = int64(9007199254740993) // 2^53 + 1, not exactly representable as float64
	p.Content.Operations[0].Precondition.ObservedModifiedIndex = big
	if err := p.Seal(); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	if err := Write(&sb, p); err != nil {
		t.Fatal(err)
	}
	got, err := Read(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Content.Operations[0].Precondition.ObservedModifiedIndex != big {
		t.Errorf("revision lost precision: %d != %d",
			got.Content.Operations[0].Precondition.ObservedModifiedIndex, big)
	}
}

// Plans are reviewed in pull requests, so the artifact has to be readable.
func TestWriteProducesIndentedOutput(t *testing.T) {
	var sb strings.Builder
	if err := Write(&sb, samplePlan(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), "\n  ") {
		t.Error("plan output is not indented; it would be unreviewable")
	}
}

func TestWriteDoesNotEscapeHTML(t *testing.T) {
	p := samplePlan(t)
	p.Content.Operations[0].Warnings = []string{"matches a < b && c > d"}
	if err := p.Seal(); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := Write(&sb, p); err != nil {
		t.Fatal(err)
	}
	for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(sb.String(), escaped) {
			t.Errorf("HTML escaping made the plan hard to read (%s): %s", escaped, sb.String())
		}
	}
	if !strings.Contains(sb.String(), "a < b && c > d") {
		t.Error("the warning text was not preserved verbatim")
	}
}
