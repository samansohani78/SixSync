package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/exitcode"
)

// planGateway serves a small fixed gateway state for plan tests.
func planGateway() *fakeGateway {
	return &fakeGateway{resources: map[string][]map[string]any{
		"routes": {
			{"id": "pay-export", "name": "payments-export", "uri": "/v1/export",
				"upstream_id": "payments-backend", "methods": []any{"POST"},
				"create_time": 1787430328, "update_time": 1787430328},
		},
		"upstreams": {
			{"id": "payments-backend", "type": "roundrobin",
				"nodes": map[string]any{"10.20.0.11:8080": 5}},
		},
	}}
}

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
	return dir
}

func TestPlanReportsNoChangesWhenMatching(t *testing.T) {
	startGateway(t, planGateway())
	dir := writeRepo(t, map[string]string{
		"routes/pay.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: pay-export\n" +
			"  name: payments-export\nlifecycle:\n  state: present\nspec:\n" +
			"  name: payments-export\n  uri: /v1/export\n  upstream_id: payments-backend\n" +
			"  methods:\n  - POST\n",
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n" +
			"  id: payments-backend\nlifecycle:\n  state: present\nspec:\n  type: roundrobin\n" +
			"  nodes:\n    10.20.0.11:8080: 5\n",
	})

	out, _, code := run(t, "plan", "--repo-dir", dir, "--detailed-exitcode")
	if code != exitcode.OK {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "No changes") {
		t.Errorf("expected no changes:\n%s", out)
	}
	if !strings.Contains(out, "This is a plan") {
		t.Error("output should make clear that nothing was changed")
	}
}

func TestPlanDetailedExitcodeOnChanges(t *testing.T) {
	startGateway(t, planGateway())
	dir := writeRepo(t, map[string]string{
		"routes/new.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-new\n" +
			"lifecycle:\n  state: present\nspec:\n  uri: /new\n  upstream_id: payments-backend\n",
	})

	// Without the flag, changes are informational.
	if _, _, code := run(t, "plan", "--repo-dir", dir); code != exitcode.OK {
		t.Errorf("plan without --detailed-exitcode should exit 0, got %d", code)
	}
	if _, _, code := run(t, "plan", "--repo-dir", dir, "--detailed-exitcode"); code != exitcode.Diff {
		t.Errorf("exit code = %d, want %d", code, exitcode.Diff)
	}
}

func TestPlanWritesArtifact(t *testing.T) {
	startGateway(t, planGateway())
	dir := writeRepo(t, map[string]string{
		"routes/new.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-new\n" +
			"lifecycle:\n  state: present\nspec:\n  uri: /new\n  upstream_id: payments-backend\n",
	})
	out := filepath.Join(t.TempDir(), "nested", "plan.json")

	if _, _, code := run(t, "plan", "--repo-dir", dir, "--out", out); code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("plan artifact not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("artifact is not valid JSON: %v", err)
	}
	if got["digest"] == "" {
		t.Error("artifact has no digest")
	}

	// A plan describes a production gateway in detail.
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("plan file mode = %o, want 600", perm)
	}
}

// The artifact must be byte-identical across runs, or reviewing one means
// nothing.
func TestPlanArtifactIsDeterministic(t *testing.T) {
	startGateway(t, planGateway())
	dir := writeRepo(t, map[string]string{
		"routes/a.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-a\n" +
			"lifecycle:\n  state: present\nspec:\n  uri: /a\n  upstream_id: payments-backend\n",
		"routes/b.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-b\n" +
			"lifecycle:\n  state: present\nspec:\n  uri: /b\n  upstream_id: payments-backend\n",
	})

	var firstDigest string
	tmp := t.TempDir()
	for i := 0; i < 6; i++ {
		out := filepath.Join(tmp, "p.json")
		if _, _, code := run(t, "plan", "--repo-dir", dir, "--out", out); code != exitcode.OK {
			t.Fatalf("run %d: exit %d", i, code)
		}
		data, _ := os.ReadFile(out)
		var p struct {
			Digest string `json:"digest"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstDigest = p.Digest
			continue
		}
		if p.Digest != firstDigest {
			t.Fatalf("digest varied between runs: %s then %s", firstDigest, p.Digest)
		}
	}
}

func TestPlanBlocksOnDanglingReference(t *testing.T) {
	startGateway(t, planGateway())
	dir := writeRepo(t, map[string]string{
		"routes/bad.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-bad\n" +
			"lifecycle:\n  state: present\nspec:\n  uri: /bad\n  upstream_id: does-not-exist\n",
	})

	out, _, code := run(t, "plan", "--repo-dir", dir)
	if code != exitcode.Failure {
		t.Errorf("exit code = %d, want %d", code, exitcode.Failure)
	}
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("blocked operations should be prominent:\n%s", out)
	}
	if !strings.Contains(out, "does-not-exist") {
		t.Errorf("the missing target should be named:\n%s", out)
	}
}

func TestPlanPolicyDenialUsesItsOwnExitCode(t *testing.T) {
	startGateway(t, planGateway())
	dir := writeRepo(t, map[string]string{
		"routes/a.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-a\n" +
			"lifecycle:\n  state: present\nspec:\n  uri: /a\n  upstream_id: payments-backend\n",
	})
	profile := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, profile, "apiVersion: sixsync/v1alpha1\nkind: PolicyProfile\n"+
		"metadata:\n  name: strict\nspec:\n  limits:\n    max_creates: 0\n")

	out, _, code := run(t, "plan", "--repo-dir", dir, "--policy", profile)
	if code != exitcode.PolicyDenied {
		t.Errorf("exit code = %d, want %d (policy denied)", code, exitcode.PolicyDenied)
	}
	if !strings.Contains(out, "max-creates") {
		t.Errorf("the violated rule should be named:\n%s", out)
	}
}

func TestPlanRejectsBadPolicyProfile(t *testing.T) {
	startGateway(t, planGateway())
	dir := writeRepo(t, map[string]string{})
	profile := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, profile, "apiVersion: wrong/v1\nkind: PolicyProfile\n")

	if _, _, code := run(t, "plan", "--repo-dir", dir, "--policy", profile); code != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (usage)", code, exitcode.Usage)
	}
}

// plan must never change remote state.
func TestPlanIssuesNoWrites(t *testing.T) {
	gw := planGateway()
	startGateway(t, gw)
	dir := writeRepo(t, map[string]string{
		"routes/new.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-new\n" +
			"lifecycle:\n  state: present\nspec:\n  uri: /new\n  upstream_id: payments-backend\n",
		"routes/gone.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: pay-export\n" +
			"lifecycle:\n  state: absent\n  reason: retired\n",
	})

	if _, _, code := run(t, "plan", "--repo-dir", dir); code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
	if gw.writes != 0 {
		t.Errorf("plan issued %d state-changing request(s)", gw.writes)
	}
}

// A plan artifact is attached to pipeline runs and pasted into reviews.
func TestPlanArtifactContainsNoCredential(t *testing.T) {
	const secret = "super-secret-admin-key-value"
	gw := planGateway()
	srv := startGateway(t, gw)
	_ = srv
	t.Setenv("SIXSYNC_ADMIN_KEY", secret)

	dir := writeRepo(t, map[string]string{
		"routes/a.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-a\n" +
			"lifecycle:\n  state: present\nspec:\n  uri: /a\n  upstream_id: payments-backend\n" +
			"  plugins:\n    key-auth:\n      key: aGVsbG93b3JsZGhlbGxvd29ybGRoZWxsb3dvcmxk\n",
	})
	out := filepath.Join(t.TempDir(), "plan.json")

	stdout, stderr, _ := run(t, "plan", "--repo-dir", dir, "--out", out)
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	for name, content := range map[string]string{
		"artifact": string(data), "stdout": stdout, "stderr": stderr,
	} {
		if strings.Contains(content, secret) {
			t.Errorf("the admin key leaked into %s", name)
		}
		if strings.Contains(content, "aGVsbG93b3JsZGhlbGxvd29ybGRoZWxsb3dvcmxk") {
			t.Errorf("a plugin credential leaked into %s", name)
		}
	}
	if !strings.Contains(string(data), "sha256:") {
		t.Error("expected a key fingerprint in the artifact")
	}
}

func TestPlanJSONOutput(t *testing.T) {
	startGateway(t, planGateway())
	dir := writeRepo(t, map[string]string{})

	out, _, code := run(t, "plan", "--repo-dir", dir, "--output", "json")
	if code != exitcode.OK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var got struct {
		Content struct {
			FormatVersion string `json:"format_version"`
		} `json:"content"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if got.Content.FormatVersion == "" || got.Digest == "" {
		t.Errorf("incomplete artifact: %+v", got)
	}
}

// The rendered form is what a reviewer reads, so it is pinned.
func TestPlanRenderedOutputGolden(t *testing.T) {
	gw := planGateway()
	startGateway(t, gw)

	dir := writeRepo(t, map[string]string{
		"routes/new.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-new\n" +
			"  name: brand new\nlifecycle:\n  state: present\nspec:\n  uri: /new\n" +
			"  upstream_id: payments-backend\n",
		"routes/changed.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n" +
			"  id: pay-export\n  name: payments-export\nlifecycle:\n  state: present\nspec:\n" +
			"  name: payments-export\n  uri: /v1/export-v2\n  upstream_id: payments-backend\n" +
			"  methods:\n  - POST\n",
	})

	out, _, _ := run(t, "plan", "--repo-dir", dir)

	// The endpoint is a random httptest port, so normalize it.
	normalized := normalizeForGolden(out)
	compareGolden(t, "plan_render.golden", normalized)
}

func normalizeForGolden(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(line, "Plan for "):
			b.WriteString("Plan for <endpoint>\n")
		case strings.HasPrefix(line, "Digest: "):
			b.WriteString("Digest: <digest>\n")
		default:
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func compareGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file: %v (run with UPDATE_GOLDEN=1 to create it)", err)
	}
	if string(want) != got {
		t.Errorf("rendered plan does not match %s\n--- want ---\n%s\n--- got ---\n%s",
			path, want, got)
	}
}

func TestPlanHelpStatesItsGuarantees(t *testing.T) {
	out, _, _ := run(t, "plan", "--help")
	for _, want := range []string{"read-only", "deterministic", "stale"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan help no longer mentions %q:\n%s", want, out)
		}
	}
}
