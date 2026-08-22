package buildinfo

import (
	"encoding/json"
	"runtime"
	"testing"
)

// Built without ldflags (as `go test` is), every field must still be
// populated with something meaningful — never an empty string, because an
// empty string in JSON output is ambiguous.
func TestGetNeverReturnsEmptyFields(t *testing.T) {
	i := Get()
	fields := map[string]string{
		"Version":   i.Version,
		"Commit":    i.Commit,
		"Date":      i.Date,
		"GoVersion": i.GoVersion,
		"Platform":  i.Platform,
	}
	for name, v := range fields {
		if v == "" {
			t.Errorf("Info.%s is empty; want a value or %q", name, Unknown)
		}
	}
}

func TestGetReportsRealRuntimeValues(t *testing.T) {
	i := Get()
	if i.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", i.GoVersion, runtime.Version())
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; i.Platform != want {
		t.Errorf("Platform = %q, want %q", i.Platform, want)
	}
}

func TestJSONShapeIsStable(t *testing.T) {
	b, err := json.Marshal(Get())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"version", "commit", "date", "go_version", "platform"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON output is missing key %q", key)
		}
	}
	if len(got) != 5 {
		t.Errorf("JSON output has %d keys, want exactly 5: %v", len(got), got)
	}
}

// Injected values must win over anything read from build metadata.
func TestInjectedValuesTakePrecedence(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	t.Cleanup(func() { version, commit, date = oldV, oldC, oldD })

	version, commit, date = "v1.2.3", "abc123", "2026-01-01T00:00:00Z"
	i := Get()
	if i.Version != "v1.2.3" || i.Commit != "abc123" || i.Date != "2026-01-01T00:00:00Z" {
		t.Errorf("injected values were not used: %+v", i)
	}
}
