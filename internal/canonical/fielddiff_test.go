package canonical

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustCanon(t *testing.T, kind string, raw string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return m
}

func pathsOf(changes []FieldChange) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, string(c.Type)+" "+c.Path)
	}
	return out
}

func TestDiffFieldsDetectsEachChangeType(t *testing.T) {
	observed := mustCanon(t, "Route", `{"uri":"/a","priority":1,"gone":"x"}`)
	desired := mustCanon(t, "Route", `{"uri":"/b","priority":1,"added":"y"}`)

	got := pathsOf(DiffFields(observed, desired))
	want := []string{"added added", "removed gone", "modified uri"}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("changes = %v, want %v", got, want)
	}
}

func TestIdenticalResourcesProduceNoChanges(t *testing.T) {
	m := mustCanon(t, "Route", `{"uri":"/a","plugins":{"cors":{}},"methods":["GET"]}`)
	if got := DiffFields(m, m); len(got) != 0 {
		t.Errorf("identical inputs produced changes: %v", got)
	}
}

func TestDiffFieldsDescendsIntoNestedMaps(t *testing.T) {
	observed := mustCanon(t, "Route", `{"plugins":{"proxy-rewrite":{"uri":"/old"}}}`)
	desired := mustCanon(t, "Route", `{"plugins":{"proxy-rewrite":{"uri":"/new"}}}`)

	got := DiffFields(observed, desired)
	if len(got) != 1 {
		t.Fatalf("expected one change, got %v", got)
	}
	if got[0].Path != "plugins.proxy-rewrite.uri" {
		t.Errorf("path = %q, want the full nested path", got[0].Path)
	}
	if got[0].Old != "/old" || got[0].New != "/new" {
		t.Errorf("old/new = %q/%q", got[0].Old, got[0].New)
	}
}

func TestDiffFieldsIndexesListElements(t *testing.T) {
	observed := mustCanon(t, "Upstream", `{"nodes":[{"host":"a"},{"host":"b"}]}`)
	desired := mustCanon(t, "Upstream", `{"nodes":[{"host":"a"},{"host":"c"}]}`)

	got := DiffFields(observed, desired)
	if len(got) != 1 {
		t.Fatalf("expected one change, got %v", got)
	}
	if got[0].Path != "nodes[1].host" {
		t.Errorf("path = %q, want nodes[1].host", got[0].Path)
	}
}

func TestDiffFieldsHandlesListLengthChanges(t *testing.T) {
	observed := mustCanon(t, "Upstream", `{"nodes":[{"host":"a"},{"host":"b"}]}`)
	desired := mustCanon(t, "Upstream", `{"nodes":[{"host":"a"}]}`)

	got := DiffFields(observed, desired)
	if len(got) != 1 || got[0].Type != ChangeRemoved {
		t.Fatalf("expected one removal, got %v", got)
	}
	// The removed element is descended into, so every value has a path the
	// redactor can recognise.
	if got[0].Path != "nodes[1].host" {
		t.Errorf("path = %q, want nodes[1].host", got[0].Path)
	}
}

// Plan output is diffed by humans and parsed by pipelines.
func TestDiffFieldsIsDeterministic(t *testing.T) {
	observed := mustCanon(t, "Route", `{"a":1,"b":2,"c":3,"d":4,"e":5,"plugins":{"x":{},"y":{}}}`)
	desired := mustCanon(t, "Route", `{"a":9,"b":8,"c":7,"d":6,"e":5,"plugins":{"y":{},"z":{}}}`)

	var first string
	for i := 0; i < 50; i++ {
		got := strings.Join(pathsOf(DiffFields(observed, desired)), ",")
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("diff ordering varied:\n%s\n%s", first, got)
		}
	}
}

func TestDiffFieldsPreservesLargeIntegerPrecision(t *testing.T) {
	observed := mustCanon(t, "Route", `{"upstream_id":552218409505391294}`)
	desired := mustCanon(t, "Route", `{"upstream_id":552218409505391295}`)

	got := DiffFields(observed, desired)
	if len(got) != 1 {
		t.Fatalf("a one-digit change in a large integer was not detected: %v", got)
	}
	if got[0].Old != "552218409505391294" || got[0].New != "552218409505391295" {
		t.Errorf("precision lost in rendering: %q -> %q", got[0].Old, got[0].New)
	}
}

func TestDiffFieldsDistinguishesNullFromAbsent(t *testing.T) {
	observed := mustCanon(t, "Route", `{"a":null}`)
	desired := mustCanon(t, "Route", `{}`)

	got := DiffFields(observed, desired)
	if len(got) != 1 || got[0].Type != ChangeRemoved {
		t.Fatalf("expected a removal, got %v", got)
	}
	if got[0].Old != "null" {
		t.Errorf("null rendered as %q", got[0].Old)
	}
}

// A type change is a real change even when the rendering looks similar.
func TestDiffFieldsDetectsTypeChanges(t *testing.T) {
	observed := mustCanon(t, "Route", `{"v":{"a":1}}`)
	desired := mustCanon(t, "Route", `{"v":"a"}`)

	if got := DiffFields(observed, desired); len(got) == 0 {
		t.Error("a map replaced by a string was not reported as a change")
	}
}

// --- redaction -------------------------------------------------------------

type prefixRedactor struct{ prefix string }

func (p prefixRedactor) RedactPath(path string) (string, bool) {
	if strings.HasPrefix(path, p.prefix) {
		return "[REDACTED]", true
	}
	return "", false
}

// A diff reaches plan artifacts, CI logs and review comments at once.
func TestRedactReplacesSensitiveValuesButKeepsPaths(t *testing.T) {
	observed := mustCanon(t, "Route", `{"plugins":{"key-auth":{"key":"old-secret"}},"uri":"/a"}`)
	desired := mustCanon(t, "Route", `{"plugins":{"key-auth":{"key":"new-secret"}},"uri":"/b"}`)

	got := Redact(DiffFields(observed, desired), prefixRedactor{prefix: "plugins.key-auth.key"})

	var sawSecret, sawURI bool
	for _, c := range got {
		if strings.Contains(c.Old, "secret") || strings.Contains(c.New, "secret") {
			t.Errorf("a secret survived redaction: %+v", c)
		}
		if c.Path == "plugins.key-auth.key" {
			sawSecret = true
			if c.Old != "[REDACTED]" || c.New != "[REDACTED]" {
				t.Errorf("expected both sides redacted: %+v", c)
			}
		}
		if c.Path == "uri" {
			sawURI = true
			if c.Old != "/a" || c.New != "/b" {
				t.Errorf("a non-sensitive field was redacted: %+v", c)
			}
		}
	}
	if !sawSecret {
		t.Error("the sensitive path disappeared entirely; knowing a secret changed is useful")
	}
	if !sawURI {
		t.Error("the non-sensitive change was lost")
	}
}

func TestRedactWithNilRedactorIsIdentity(t *testing.T) {
	changes := []FieldChange{{Path: "a", Type: ChangeModified, Old: "1", New: "2"}}
	got := Redact(changes, nil)
	if len(got) != 1 || got[0].Old != "1" {
		t.Errorf("nil redactor altered the changes: %+v", got)
	}
}

func TestRedactDoesNotInventValues(t *testing.T) {
	// An added field has no Old; redaction must not fabricate one.
	changes := []FieldChange{{Path: "secret", Type: ChangeAdded, New: "v"}}
	got := Redact(changes, prefixRedactor{prefix: "secret"})
	if got[0].Old != "" {
		t.Errorf("redaction invented an Old value: %+v", got[0])
	}
	if got[0].New != "[REDACTED]" {
		t.Errorf("New was not redacted: %+v", got[0])
	}
}

// Rendering an added subtree as a single value would defeat path-based
// redaction: a credential nested inside it would be serialized into the change
// for its parent, a path no secret rule matches, and would then appear in the
// plan artifact and in CI logs.
//
// This is a regression test for a real leak.
func TestAddedSubtreesAreDescendedSoSecretsRemainRedactable(t *testing.T) {
	desired := mustCanon(t, "Route", `{
		"uri":"/pay",
		"plugins":{"key-auth":{"key":"a-real-credential"},"cors":{}}
	}`)

	// A create diffs against nothing.
	changes := DiffFields(nil, desired)

	var sawLeaf bool
	for _, c := range changes {
		if c.Path == "plugins.key-auth.key" {
			sawLeaf = true
		}
		// No change may carry the credential at a path that is not the
		// credential's own, or redaction cannot reach it.
		if strings.Contains(c.New, "a-real-credential") && c.Path != "plugins.key-auth.key" {
			t.Errorf("the credential appears at path %q, where redaction cannot match it: %+v",
				c.Path, c)
		}
	}
	if !sawLeaf {
		t.Fatalf("expected a leaf change at plugins.key-auth.key, got %+v", changes)
	}

	// With redaction applied, the value must be gone entirely.
	got := Redact(changes, prefixRedactor{prefix: "plugins.key-auth.key"})
	for _, c := range got {
		if strings.Contains(c.Old, "a-real-credential") || strings.Contains(c.New, "a-real-credential") {
			t.Errorf("the credential survived redaction at %q: %+v", c.Path, c)
		}
	}
}

// An empty map is meaningful in APISIX — `plugins: {cors: {}}` enables a plugin
// with no configuration — so it must not be dropped when descending.
func TestEmptyCompositesAreStillReported(t *testing.T) {
	desired := mustCanon(t, "Route", `{"plugins":{"cors":{}},"tags":[]}`)
	changes := DiffFields(nil, desired)

	paths := map[string]string{}
	for _, c := range changes {
		paths[c.Path] = c.New
	}
	if paths["plugins.cors"] != "{}" {
		t.Errorf("an empty plugin block was not reported: %+v", changes)
	}
	if paths["tags"] != "[]" {
		t.Errorf("an empty list was not reported: %+v", changes)
	}
}

// Removed subtrees need the same treatment: a credential being removed must not
// be printed on its way out.
func TestRemovedSubtreesAreDescended(t *testing.T) {
	observed := mustCanon(t, "Route", `{"plugins":{"key-auth":{"key":"old-credential"}}}`)
	changes := DiffFields(observed, mustCanon(t, "Route", `{}`))

	for _, c := range changes {
		if strings.Contains(c.Old, "old-credential") && c.Path != "plugins.key-auth.key" {
			t.Errorf("the removed credential appears at %q: %+v", c.Path, c)
		}
	}
	got := Redact(changes, prefixRedactor{prefix: "plugins.key-auth.key"})
	for _, c := range got {
		if strings.Contains(c.Old, "old-credential") {
			t.Errorf("a removed credential survived redaction: %+v", c)
		}
	}
}

// A type change leaves one side composite. Rendering that side whole would
// serialize every value inside it at the parent's path — the same redaction
// bypass added and removed subtrees had.
//
// Found by FuzzDiffFields.
func TestTypeChangeDoesNotRenderACompositeWhole(t *testing.T) {
	observed := mustCanon(t, "Route", `{"plugins":{"key-auth":{"key":"a-real-credential"}}}`)
	desired := mustCanon(t, "Route", `{"plugins":"replaced-by-a-string"}`)

	changes := DiffFields(observed, desired)

	for _, c := range changes {
		if strings.Contains(c.Old, "a-real-credential") && c.Path != "plugins.key-auth.key" {
			t.Errorf("the credential appears at path %q, where redaction cannot match it: %+v",
				c.Path, c)
		}
	}

	got := Redact(changes, prefixRedactor{prefix: "plugins.key-auth.key"})
	for _, c := range got {
		if strings.Contains(c.Old, "a-real-credential") || strings.Contains(c.New, "a-real-credential") {
			t.Errorf("the credential survived redaction at %q: %+v", c.Path, c)
		}
	}

	// The replacement value must still be visible.
	var sawNew bool
	for _, c := range got {
		if c.New == "replaced-by-a-string" {
			sawNew = true
		}
	}
	if !sawNew {
		t.Errorf("the new value was lost: %+v", got)
	}
}

// The reverse direction: a scalar replaced by a composite holding a secret.
func TestScalarReplacedByCompositeIsDescended(t *testing.T) {
	observed := mustCanon(t, "Route", `{"plugins":"was-a-string"}`)
	desired := mustCanon(t, "Route", `{"plugins":{"key-auth":{"key":"brand-new-credential"}}}`)

	changes := DiffFields(observed, desired)
	for _, c := range changes {
		if strings.Contains(c.New, "brand-new-credential") && c.Path != "plugins.key-auth.key" {
			t.Errorf("the credential appears at path %q: %+v", c.Path, c)
		}
	}
}
