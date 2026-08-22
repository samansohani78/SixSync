package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/exitcode"
)

// fakeGateway serves the response shapes a real APISIX 3.18.0 produces, so the
// command layer is exercised end to end without a container.
type fakeGateway struct {
	// resources maps an Admin API path segment to the resource bodies it holds.
	resources map[string][]map[string]any

	// writes counts requests that would change state. Read-only commands must
	// leave this at zero.
	writes int
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{resources: map[string][]map[string]any{
		"routes": {
			{"id": "pay-export", "name": "payments-export", "uri": "/v1/export",
				"upstream_id": "payments-backend", "methods": []any{"POST", "OPTIONS"},
				"create_time": 1787430328, "update_time": 1787430328},
		},
		"upstreams": {
			{"id": "payments-backend", "type": "roundrobin",
				"nodes":       map[string]any{"10.20.0.11:8080": 5},
				"create_time": 1787430328, "update_time": 1787430328},
		},
	}}
}

func (f *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "APISIX/3.18.0")
	w.Header().Set("X-API-VERSION", "v3")
	w.Header().Set("Content-Type", "application/json")

	if r.Header.Get("X-API-KEY") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error_msg":"failed to check token","description":"missing apikey"}`)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/apisix/admin/schema/validate/") {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error_msg":"object matches none of the required"}`)
		return
	}

	// The capability probe is a DELETE against an identifier APISIX can never
	// store, so it cannot change state and is not counted as a write.
	if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, apisix.CapabilityProbeID) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Key not found"}`)
		return
	}

	if r.Method != http.MethodGet {
		f.writes++
		w.WriteHeader(http.StatusOK)
		return
	}

	kind := strings.TrimPrefix(r.URL.Path, "/apisix/admin/")
	items := make([]map[string]any, 0)
	for _, v := range f.resources[kind] {
		id, _ := v["id"].(string)
		items = append(items, map[string]any{
			"key": "/apisix/" + kind + "/" + id, "value": v,
			"createdIndex": 10, "modifiedIndex": 20,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"list": items, "total": len(items)})
}

func startGateway(t *testing.T, f *fakeGateway) string {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	t.Setenv("SIXSYNC_ADMIN_URL", srv.URL)
	t.Setenv("SIXSYNC_ADMIN_KEY", "test-key")
	return srv.URL
}

func TestDoctorReportsAHealthyEndpoint(t *testing.T) {
	startGateway(t, newFakeGateway())

	out, _, code := run(t, "doctor")
	if code != exitcode.OK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{"Reachable:           yes", "3.18.0", "v3"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output is missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorJSON(t *testing.T) {
	startGateway(t, newFakeGateway())

	out, _, _ := run(t, "doctor", "--output", "json")
	var got struct {
		Reachable     bool   `json:"Reachable"`
		APISIXVersion string `json:"APISIXVersion"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if !got.Reachable || got.APISIXVersion != "3.18.0" {
		t.Errorf("unexpected diagnosis: %+v", got)
	}
}

func TestImportWritesResourcesPreservingIdentifiers(t *testing.T) {
	startGateway(t, newFakeGateway())
	dir := t.TempDir()

	out, _, code := run(t, "import", "--repo-dir", dir)
	if code != exitcode.OK {
		t.Fatalf("exit %d\n%s", code, out)
	}

	routePath := filepath.Join(dir, "routes", "payments-export-pay-export.yaml")
	data, err := os.ReadFile(routePath)
	if err != nil {
		t.Fatalf("expected an imported route at %s: %v", routePath, err)
	}
	content := string(data)

	if !strings.Contains(content, "id: pay-export") {
		t.Errorf("the APISIX identifier was not preserved:\n%s", content)
	}
	// Server-owned timestamps must not reach the file; APISIX rejects a write
	// that carries them.
	for _, forbidden := range []string{"create_time", "update_time"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("%s was written to the file:\n%s", forbidden, content)
		}
	}
	// References must stay references, not be inlined.
	if !strings.Contains(content, "upstream_id: payments-backend") {
		t.Errorf("the upstream reference was not preserved:\n%s", content)
	}
}

func TestImportDryRunTouchesNothing(t *testing.T) {
	startGateway(t, newFakeGateway())
	dir := t.TempDir()

	out, _, code := run(t, "import", "--repo-dir", dir, "--dry-run")
	if code != exitcode.OK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "Nothing was changed") {
		t.Errorf("a dry run should say so:\n%s", out)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a dry run wrote to the filesystem: %v", entries)
	}
}

// An import onto a populated directory must not discard local edits that have
// not been applied yet.
func TestImportDoesNotOverwriteByDefault(t *testing.T) {
	startGateway(t, newFakeGateway())
	dir := t.TempDir()

	if _, _, code := run(t, "import", "--repo-dir", dir); code != exitcode.OK {
		t.Fatal("first import failed")
	}

	routePath := filepath.Join(dir, "routes", "payments-export-pay-export.yaml")
	edited := "# a local edit that has not been applied yet\n"
	original, _ := os.ReadFile(routePath)
	if err := os.WriteFile(routePath, append([]byte(edited), original...), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, _ := run(t, "import", "--repo-dir", dir)
	if !strings.Contains(out, "already exists") {
		t.Errorf("expected the existing file to be skipped:\n%s", out)
	}

	after, _ := os.ReadFile(routePath)
	if !strings.Contains(string(after), "a local edit") {
		t.Error("the local edit was silently overwritten")
	}

	// With --overwrite it should be replaced.
	if _, _, code := run(t, "import", "--repo-dir", dir, "--overwrite"); code != exitcode.OK {
		t.Fatal("import --overwrite failed")
	}
	after, _ = os.ReadFile(routePath)
	if strings.Contains(string(after), "a local edit") {
		t.Error("--overwrite did not replace the file")
	}
}

// The round-trip property the whole tool rests on.
func TestImportThenDriftReportsNoDifferences(t *testing.T) {
	startGateway(t, newFakeGateway())
	dir := t.TempDir()

	if _, _, code := run(t, "import", "--repo-dir", dir); code != exitcode.OK {
		t.Fatal("import failed")
	}

	out, _, code := run(t, "drift", "--repo-dir", dir, "--detailed-exitcode")
	if code != exitcode.OK {
		t.Errorf("an imported repository reported drift (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "No differences") {
		t.Errorf("unexpected drift output:\n%s", out)
	}
}

// Two imports must be byte-identical, or re-running produces spurious diffs.
func TestImportIsDeterministic(t *testing.T) {
	startGateway(t, newFakeGateway())
	a, b := t.TempDir(), t.TempDir()

	if _, _, code := run(t, "import", "--repo-dir", a); code != exitcode.OK {
		t.Fatal("first import failed")
	}
	if _, _, code := run(t, "import", "--repo-dir", b); code != exitcode.OK {
		t.Fatal("second import failed")
	}

	_ = filepath.Walk(a, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(a, path)
		want, _ := os.ReadFile(path)
		got, readErr := os.ReadFile(filepath.Join(b, rel))
		if readErr != nil {
			t.Errorf("%s is missing from the second import", rel)
			return nil
		}
		if string(want) != string(got) {
			t.Errorf("%s differs between imports:\n--- a ---\n%s\n--- b ---\n%s", rel, want, got)
		}
		return nil
	})
}

func TestDriftClassifiesDifferences(t *testing.T) {
	gw := newFakeGateway()
	startGateway(t, gw)
	dir := t.TempDir()

	if _, _, code := run(t, "import", "--repo-dir", dir); code != exitcode.OK {
		t.Fatal("import failed")
	}

	// A local file describing something the gateway does not have.
	writeFile(t, filepath.Join(dir, "routes", "ghost.yaml"),
		"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: ghost\n"+
			"lifecycle:\n  state: present\nspec:\n  uri: /ghost\n")

	// A gateway resource no file describes.
	gw.resources["routes"] = append(gw.resources["routes"], map[string]any{
		"id": "unmanaged-route", "uri": "/unmanaged",
	})

	out, _, code := run(t, "drift", "--repo-dir", dir, "--detailed-exitcode")
	if code != exitcode.Diff {
		t.Errorf("exit code = %d, want %d", code, exitcode.Diff)
	}
	if !strings.Contains(out, "missing-remote  Route          ghost") {
		t.Errorf("expected a missing-remote finding:\n%s", out)
	}
	if !strings.Contains(out, "unmanaged       Route          unmanaged-route") {
		t.Errorf("expected an unmanaged finding:\n%s", out)
	}
	// An unmanaged resource must never be presented as deletable.
	if !strings.Contains(out, "never") {
		t.Errorf("output should state that unmanaged is not a deletion:\n%s", out)
	}
}

// Differences are normal; only an opt-in flag turns them into a failing status.
func TestDriftExitsZeroWithoutDetailedExitcode(t *testing.T) {
	gw := newFakeGateway()
	startGateway(t, gw)
	dir := t.TempDir()

	if _, _, code := run(t, "drift", "--repo-dir", dir); code != exitcode.OK {
		t.Errorf("drift without --detailed-exitcode should exit 0, got %d", code)
	}
}

// Every read-only command must leave the gateway untouched.
func TestReadOnlyCommandsIssueNoWrites(t *testing.T) {
	commands := [][]string{
		{"doctor"},
		{"import"},
		{"drift"},
		{"validate"},
	}
	for _, args := range commands {
		t.Run(args[0], func(t *testing.T) {
			gw := newFakeGateway()
			startGateway(t, gw)
			dir := t.TempDir()

			_, _, _ = run(t, append(args, "--repo-dir", dir)...)

			if gw.writes != 0 {
				t.Errorf("%s issued %d state-changing request(s)", args[0], gw.writes)
			}
		})
	}
}

func TestDriftReportsUnreadableFiles(t *testing.T) {
	startGateway(t, newFakeGateway())
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "routes", "broken.yaml"), "kind: Route\n  broken: [")

	_, errOut, code := run(t, "drift", "--repo-dir", dir)
	if code != exitcode.Failure {
		t.Errorf("exit code = %d, want %d", code, exitcode.Failure)
	}
	if !strings.Contains(errOut, "cannot read") {
		t.Errorf("expected the unreadable file to be named: %s", errOut)
	}
}

func TestValidateLocalOnlyNeedsNoGateway(t *testing.T) {
	t.Setenv("SIXSYNC_ADMIN_URL", "")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "routes", "a.yaml"),
		"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: route-a\n"+
			"lifecycle:\n  state: present\nspec:\n  uri: /a\n")

	out, _, code := run(t, "validate", "--repo-dir", dir)
	if code != exitcode.OK {
		t.Errorf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "Local checks only") {
		t.Errorf("output should be clear about what was and was not checked:\n%s", out)
	}
}

func TestImportJSONOutput(t *testing.T) {
	startGateway(t, newFakeGateway())
	dir := t.TempDir()

	out, _, _ := run(t, "import", "--repo-dir", dir, "--output", "json", "--dry-run")

	var got struct {
		DryRun    bool `json:"dry_run"`
		Resources []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if !got.DryRun {
		t.Error("dry_run should be true")
	}
	if len(got.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(got.Resources))
	}
}

func TestImportKindFilter(t *testing.T) {
	startGateway(t, newFakeGateway())
	dir := t.TempDir()

	out, _, code := run(t, "import", "--repo-dir", dir, "--kind", "Upstream")
	if code != exitcode.OK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "routes")); !os.IsNotExist(err) {
		t.Error("--kind Upstream should not have imported routes")
	}
	if _, err := os.Stat(filepath.Join(dir, "upstreams")); err != nil {
		t.Error("--kind Upstream did not import upstreams")
	}
}

func TestCommandsReportAuthenticationFailureClearly(t *testing.T) {
	gw := newFakeGateway()
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)

	t.Setenv("SIXSYNC_ADMIN_URL", srv.URL)
	t.Setenv("SIXSYNC_ADMIN_KEY", "") // the fake rejects an empty key
	t.Setenv("APISIX_ADMIN_API_KEY", "")

	out, _, code := run(t, "doctor")
	if code != exitcode.Failure {
		t.Errorf("exit code = %d, want %d", code, exitcode.Failure)
	}
	if !strings.Contains(out, "no API key") {
		t.Errorf("expected a clear diagnosis:\n%s", out)
	}
}
