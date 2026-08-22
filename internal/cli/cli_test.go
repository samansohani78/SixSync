package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/exitcode"
)

// run executes the command tree and returns stdout, stderr and the exit code.
func run(t *testing.T, args ...string) (string, string, exitcode.Code) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Execute(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestVersionText(t *testing.T) {
	out, _, code := run(t, "version")
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, want := range []string{"sixsync", "commit:", "built:", "go:", "platform:"} {
		if !strings.Contains(out, want) {
			t.Errorf("version output is missing %q:\n%s", want, out)
		}
	}
}

func TestVersionJSON(t *testing.T) {
	out, _, code := run(t, "version", "-o", "json")
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"version", "commit", "date", "go_version", "platform"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON output is missing %q", key)
		}
	}
}

// version must work before any endpoint or credential exists.
func TestVersionNeedsNoConfiguration(t *testing.T) {
	t.Setenv("SIXSYNC_ADMIN_URL", "")
	t.Setenv("SIXSYNC_ADMIN_KEY", "")
	if _, _, code := run(t, "version"); code != exitcode.OK {
		t.Errorf("version failed without configuration: exit %d", code)
	}
}

// The root command must not do anything when invoked bare.
func TestBareInvocationPrintsHelp(t *testing.T) {
	out, _, code := run(t)
	if code != exitcode.OK {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "Available Commands") {
		t.Errorf("expected help output:\n%s", out)
	}
}

func TestUnknownCommandIsAnError(t *testing.T) {
	_, _, code := run(t, "nonexistent-command")
	if code == exitcode.OK {
		t.Error("an unknown command should not exit 0")
	}
}

// Commands needing an endpoint must say every way to supply one.
func TestMissingAdminURLIsAUsageError(t *testing.T) {
	t.Setenv("SIXSYNC_ADMIN_URL", "")
	t.Setenv("APISIX_ADMIN_API_URL", "")

	for _, cmd := range []string{"doctor", "drift", "import"} {
		t.Run(cmd, func(t *testing.T) {
			_, errOut, code := run(t, cmd)
			if code != exitcode.Usage {
				t.Errorf("exit code = %d, want %d (usage)", code, exitcode.Usage)
			}
			if !strings.Contains(errOut, "--admin-url") {
				t.Errorf("error should name the flag: %s", errOut)
			}
		})
	}
}

// lint must run with no endpoint and no credential, so it is safe on a pull
// request from a fork.
func TestLintNeedsNoEndpointOrCredential(t *testing.T) {
	t.Setenv("SIXSYNC_ADMIN_URL", "")
	t.Setenv("SIXSYNC_ADMIN_KEY", "")

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "routes/a.yaml"),
		"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: route-a\n"+
			"lifecycle:\n  state: present\nspec:\n  uri: /a\n")

	out, _, code := run(t, "lint", "--repo-dir", dir)
	if code != exitcode.OK {
		t.Errorf("exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "1 file(s) checked") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestLintReportsErrorsAndFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "routes/a.yaml"),
		"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: dup\n"+
			"lifecycle:\n  state: present\nspec:\n  uri: /a\n")
	writeFile(t, filepath.Join(dir, "routes/b.yaml"),
		"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: dup\n"+
			"lifecycle:\n  state: present\nspec:\n  uri: /b\n")

	out, _, code := run(t, "lint", "--repo-dir", dir)
	if code != exitcode.Failure {
		t.Errorf("exit code = %d, want %d", code, exitcode.Failure)
	}
	if !strings.Contains(out, "duplicate-id") {
		t.Errorf("expected a duplicate-id finding:\n%s", out)
	}
}

// Warnings alone must not fail a build unless the operator opts in.
func TestLintStrictTreatsWarningsAsFailures(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "routes/a.yaml"),
		"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: route-a\n"+
			"lifecycle:\n  state: present\nspec:\n  uri: /a\n  upstream_id: missing\n")

	if _, _, code := run(t, "lint", "--repo-dir", dir); code != exitcode.OK {
		t.Errorf("warnings alone should not fail: exit %d", code)
	}
	if _, _, code := run(t, "lint", "--repo-dir", dir, "--strict"); code != exitcode.Failure {
		t.Errorf("--strict should fail on warnings: exit %d", code)
	}
}

func TestLintJSONOutputIsValid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "routes/a.yaml"),
		"apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: route-a\n"+
			"lifecycle:\n  state: present\nspec:\n  uri: /a\n  ttl: 60\n")

	out, _, _ := run(t, "lint", "--repo-dir", dir, "--output", "json")

	var got struct {
		FilesChecked int `json:"files_checked"`
		Findings     []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got.FilesChecked != 1 {
		t.Errorf("files_checked = %d, want 1", got.FilesChecked)
	}
	if len(got.Findings) != 1 || got.Findings[0].Rule != "ephemeral-route" {
		t.Errorf("unexpected findings: %+v", got.Findings)
	}
}

// The warning must appear even when stdout is captured for machine reading.
func TestInsecureTLSWarningGoesToStderr(t *testing.T) {
	dir := t.TempDir()
	out, errOut, _ := run(t, "lint", "--repo-dir", dir, "--insecure-skip-tls-verify")

	if !strings.Contains(errOut, "WARNING") {
		t.Errorf("expected a warning on stderr, got: %q", errOut)
	}
	if !strings.Contains(errOut, "intercepted") {
		t.Errorf("the warning should explain the consequence: %q", errOut)
	}
	if strings.Contains(out, "WARNING") {
		t.Error("the warning must not pollute stdout, which may be parsed")
	}
}

func TestBadOutputFormatIsAUsageError(t *testing.T) {
	_, _, code := run(t, "lint", "--output", "xml")
	if code != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (usage)", code, exitcode.Usage)
	}
}

func TestImportRefusesSecretBearingKinds(t *testing.T) {
	t.Setenv("SIXSYNC_ADMIN_URL", "http://127.0.0.1:1")
	_, errOut, code := run(t, "import", "--kind", "Consumer")

	if code != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (usage)", code, exitcode.Usage)
	}
	if !strings.Contains(errOut, "cannot be imported") {
		t.Errorf("expected a refusal: %s", errOut)
	}
	// The refusal must explain itself.
	if !strings.Contains(errOut, "credentials") {
		t.Errorf("the refusal should give the reason: %s", errOut)
	}
}

func TestImportRejectsUnknownKind(t *testing.T) {
	t.Setenv("SIXSYNC_ADMIN_URL", "http://127.0.0.1:1")
	_, errOut, code := run(t, "import", "--kind", "Nonsense")
	if code != exitcode.Usage {
		t.Errorf("exit code = %d, want %d", code, exitcode.Usage)
	}
	if !strings.Contains(errOut, "unknown kind") {
		t.Errorf("unhelpful error: %s", errOut)
	}
}

// Help text is the contract users read; these promises must not silently
// disappear.
func TestHelpStatesTheSafetyPropertiesThatMatter(t *testing.T) {
	tests := []struct {
		cmd  []string
		want []string
	}{
		{[]string{"doctor", "--help"}, []string{"never changes remote state"}},
		{[]string{"drift", "--help"}, []string{"changes nothing", "never as something to delete"}},
		{[]string{"import", "--help"}, []string{"never overwritten", "preserved exactly"}},
		{[]string{"validate", "--help"}, []string{"stores nothing"}},
		{[]string{"lint", "--help"}, []string{"no credential"}},
	}
	for _, tt := range tests {
		t.Run(tt.cmd[0], func(t *testing.T) {
			out, _, _ := run(t, tt.cmd...)
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("%s help no longer states %q:\n%s", tt.cmd[0], want, out)
				}
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
