package resource

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/model"
)

func validRoute() *File {
	return &File{
		APIVersion: APIVersion,
		Kind:       model.KindRoute,
		Metadata:   Metadata{ID: "r-payments", Name: "payments-export"},
		Lifecycle:  Lifecycle{State: StatePresent},
		Spec: map[string]any{
			"uri":         "/v1/report/export",
			"methods":     []any{"POST"},
			"upstream_id": "u-payments",
		},
	}
}

func TestDecodeAcceptsAWellFormedFile(t *testing.T) {
	in := []byte(`
apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: r-payments
  name: payments-export
lifecycle:
  state: present
spec:
  uri: /v1/report/export
  methods: [POST]
  upstream_id: u-payments
`)
	f, err := Decode(in)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if f.Kind != model.KindRoute || f.Metadata.ID != "r-payments" {
		t.Errorf("decoded incorrectly: %+v", f)
	}
	if f.Spec["uri"] != "/v1/report/export" {
		t.Errorf("spec not decoded: %#v", f.Spec)
	}
}

// An unrecognized top-level key is SixSync's own vocabulary, so it means a typo
// or a format mismatch. Ignoring it would discard operator intent.
func TestDecodeRejectsUnknownTopLevelFields(t *testing.T) {
	in := []byte(`
apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: r1
lifecycle:
  state: present
spec:
  uri: /x
lifecycl: oops
`)
	if _, err := Decode(in); err == nil {
		t.Fatal("expected an error for an unknown top-level field")
	}
}

// APISIX gains fields between releases; dropping them would corrupt config.
func TestDecodePreservesUnknownSpecFields(t *testing.T) {
	in := []byte(`
apiVersion: sixsync/v1alpha1
kind: Route
metadata:
  id: r1
lifecycle:
  state: present
spec:
  uri: /x
  upstream_id: u1
  a_field_from_a_future_apisix:
    nested: true
`)
	f, err := Decode(in)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if _, present := f.Spec["a_field_from_a_future_apisix"]; !present {
		t.Error("an unrecognized spec field was dropped")
	}
}

func TestDecodeRejectsMalformedYAML(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"not yaml at all", "\t\tthis: [is: broken"},
		{"tab indentation", "apiVersion: sixsync/v1alpha1\n\tkind: Route"},
		{"empty document", ""},
		{"scalar document", "just-a-string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode([]byte(tt.in)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*File)
		wantErr string
	}{
		{"valid", func(*File) {}, ""},
		{
			"wrong apiVersion",
			func(f *File) { f.APIVersion = "sixsync/v2" },
			"unsupported apiVersion",
		},
		{
			"unknown kind",
			func(f *File) { f.Kind = "Nonsense" },
			"unknown kind",
		},
		{
			"unsupported kind is refused with a reason",
			func(f *File) { f.Kind = model.KindConsumer },
			"not supported",
		},
		{
			"missing id",
			func(f *File) { f.Metadata.ID = "" },
			"metadata.id",
		},
		{
			"id with characters APISIX rejects",
			func(f *File) { f.Metadata.ID = "has spaces!" },
			"metadata.id",
		},
		{
			"id longer than APISIX allows",
			func(f *File) { f.Metadata.ID = strings.Repeat("a", 65) },
			"at most 64",
		},
		{
			"missing lifecycle state",
			func(f *File) { f.Lifecycle.State = "" },
			"lifecycle.state is required",
		},
		{
			"invalid lifecycle state",
			func(f *File) { f.Lifecycle.State = "deleted" },
			"not valid",
		},
		{
			"present with empty spec",
			func(f *File) { f.Spec = nil },
			"spec is empty",
		},
		{
			"create_time in spec",
			func(f *File) { f.Spec["create_time"] = 123 },
			"create_time",
		},
		{
			"update_time in spec",
			func(f *File) { f.Spec["update_time"] = 123 },
			"update_time",
		},
		{
			"id duplicated into spec",
			func(f *File) { f.Spec["id"] = "r-payments" },
			"duplicates metadata.id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := validRoute()
			tt.mutate(f)
			err := f.Validate()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

// A tombstone identifies a resource by kind and ID, so it needs no spec.
func TestAbsentStateNeedsNoSpec(t *testing.T) {
	f := validRoute()
	f.Lifecycle = Lifecycle{State: StateAbsent, Reason: "endpoint retired"}
	f.Spec = nil

	if err := f.Validate(); err != nil {
		t.Errorf("a tombstone should be valid without a spec: %v", err)
	}
}

func TestValidateID(t *testing.T) {
	tests := []struct {
		id   string
		ok   bool
		note string
	}{
		{"r1", true, "simple"},
		{"payments-export", true, "dashes"},
		{"payments_export.v2", true, "underscore and dot"},
		{"00000000000000123456", true, "APISIX POST-generated id"},
		{"633380969088287422", true, "numeric id as a string"},
		{strings.Repeat("a", 64), true, "at the limit"},
		{"", false, "empty"},
		{strings.Repeat("a", 65), false, "over the limit"},
		{"has space", false, "space"},
		{"has/slash", false, "slash would break the URL path"},
		{"has:colon", false, "colon"},
		{"café", false, "non-ASCII"},
	}
	for _, tt := range tests {
		t.Run(tt.note, func(t *testing.T) {
			err := ValidateID(tt.id)
			if tt.ok && err != nil {
				t.Errorf("ValidateID(%q) rejected a valid APISIX id: %v", tt.id, err)
			}
			if !tt.ok && err == nil {
				t.Errorf("ValidateID(%q) accepted an id APISIX would reject", tt.id)
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := validRoute()
	original.Metadata.Annotations = map[string]string{"owner": "payments"}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode of encoded output: %v\n%s", err, encoded)
	}

	if decoded.Metadata.ID != original.Metadata.ID ||
		decoded.Metadata.Name != original.Metadata.Name ||
		decoded.Kind != original.Kind ||
		decoded.Lifecycle.State != original.Lifecycle.State {
		t.Errorf("round trip changed the wrapper:\n original %+v\n decoded  %+v", original, decoded)
	}
	if decoded.Metadata.Annotations["owner"] != "payments" {
		t.Error("annotations were lost in the round trip")
	}
	if decoded.Spec["uri"] != original.Spec["uri"] {
		t.Errorf("round trip changed spec: %#v", decoded.Spec)
	}
}

// Without this, import re-runs would produce spurious diffs and golden files
// would be worthless.
func TestEncodeIsDeterministic(t *testing.T) {
	f := validRoute()
	f.Spec = map[string]any{
		"uri":         "/x",
		"upstream_id": "u1",
		"methods":     []any{"GET", "POST"},
		"plugins": map[string]any{
			"proxy-rewrite":    map[string]any{"uri": "/internal"},
			"limit-count":      map[string]any{"count": 100, "time_window": 60},
			"cors":             map[string]any{},
			"request-id":       map[string]any{},
			"response-rewrite": map[string]any{"headers": map[string]any{"X-A": "1", "X-B": "2"}},
		},
	}

	first, err := Encode(f)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for i := 0; i < 100; i++ {
		again, err := Encode(f)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("encoding varied between runs:\n--- first ---\n%s\n--- run %d ---\n%s", first, i, again)
		}
	}
}

func TestEncodeRefusesInvalidFiles(t *testing.T) {
	f := validRoute()
	f.Metadata.ID = "not a valid id"
	if _, err := Encode(f); err == nil {
		t.Fatal("Encode wrote a file that could never be applied")
	}
}

func TestFileName(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"payments-export", "r-payments", "payments-export-r-payments.yaml"},
		{"", "r-payments", "r-payments.yaml"},
		{"Payments Export!", "r1", "payments-export-r1.yaml"},
		{"r1", "r1", "r1.yaml"},
		{"", "00000000000000123456", "00000000000000123456.yaml"},
		{strings.Repeat("x", 200), "r1", strings.Repeat("x", 60) + "-r1.yaml"},
	}
	for _, tt := range tests {
		f := validRoute()
		f.Metadata.Name, f.Metadata.ID = tt.name, tt.id
		if got := FileName(f); got != tt.want {
			t.Errorf("FileName(name=%q, id=%q) = %q, want %q", tt.name, tt.id, got, tt.want)
		}
	}
}

// Two resources sharing a display name must not collide on disk.
func TestFileNamesAreUniquePerID(t *testing.T) {
	a, b := validRoute(), validRoute()
	a.Metadata.ID, b.Metadata.ID = "r-one", "r-two"
	a.Metadata.Name, b.Metadata.Name = "same-label", "same-label"

	if FileName(a) == FileName(b) {
		t.Errorf("two resources with the same name collided on %q", FileName(a))
	}
}

func TestDirName(t *testing.T) {
	got, err := DirName(model.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if got != "routes" {
		t.Errorf("DirName(Route) = %q, want %q", got, "routes")
	}
	if _, err := DirName(model.Kind("Nonsense")); err == nil {
		t.Error("expected an error for an unknown kind")
	}
}

// json.Number is a string type. Handed straight to a YAML encoder it comes out
// quoted, and APISIX rejects a port written as "9090".
func TestNumbersAreNotQuotedInYAML(t *testing.T) {
	f := validRoute()
	f.Spec = map[string]any{
		"uri":      "/x",
		"priority": json.Number("10"),
		"upstream": map[string]any{
			"type": "roundrobin",
			"nodes": []any{
				map[string]any{
					"host":   "10.0.0.1",
					"port":   json.Number("9090"),
					"weight": json.Number("1"),
				},
			},
		},
		"timeout": map[string]any{"connect": json.Number("0.5")},
	}

	out, err := Encode(f)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got := string(out)

	for _, quoted := range []string{`"9090"`, `"1"`, `"10"`, `"0.5"`} {
		if strings.Contains(got, quoted) {
			t.Errorf("a number was serialized as the quoted string %s:\n%s", quoted, got)
		}
	}
	for _, bare := range []string{"port: 9090", "weight: 1", "priority: 10", "connect: 0.5"} {
		if !strings.Contains(got, bare) {
			t.Errorf("expected %q in the output:\n%s", bare, got)
		}
	}
}

// A number written to a file must come back as a number, not a string.
func TestNumbersSurviveTheRoundTrip(t *testing.T) {
	f := validRoute()
	f.Spec = map[string]any{
		"uri":         "/x",
		"upstream_id": "u1",
		"priority":    json.Number("10"),
	}

	encoded, err := Encode(f)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	switch v := decoded.Spec["priority"].(type) {
	case string:
		t.Errorf("priority came back as the string %q; it must remain numeric", v)
	case nil:
		t.Error("priority was lost in the round trip")
	}
}

// float64 cannot hold this exactly, so any conversion through it corrupts the
// value silently.
func TestLargeIntegersKeepFullPrecisionOnEncode(t *testing.T) {
	const big = "552218409505391294"
	f := validRoute()
	f.Spec = map[string]any{"uri": "/x", "upstream_id": json.Number(big)}

	out, err := Encode(f)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(out), big) {
		t.Errorf("precision was lost encoding a large integer:\n%s", out)
	}
}
