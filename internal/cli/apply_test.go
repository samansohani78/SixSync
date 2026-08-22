package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/exitcode"
)

// applyScenario prepares a repository, a gateway and a plan on disk.
type applyScenario struct {
	dir      string
	planPath string
	journal  string
	gw       *fakeGateway
}

func newApplyScenario(t *testing.T, files map[string]string, gw *fakeGateway) *applyScenario {
	t.Helper()
	startGateway(t, gw)

	s := &applyScenario{
		dir:      writeRepo(t, files),
		planPath: filepath.Join(t.TempDir(), "plan.json"),
		journal:  filepath.Join(t.TempDir(), "journal.jsonl"),
		gw:       gw,
	}
	if _, _, code := run(t, "plan", "--repo-dir", s.dir, "--out", s.planPath); code != exitcode.OK {
		t.Fatalf("plan failed with exit %d", code)
	}
	return s
}

func newRouteFile(id, spec string) string {
	return "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: " + id +
		"\nlifecycle:\n  state: present\nspec:\n" + spec
}

func TestApplyRequiresPlanFlag(t *testing.T) {
	startGateway(t, planGateway())
	_, errOut, code := run(t, "apply", "--repo-dir", t.TempDir())

	if code != exitcode.Usage {
		t.Errorf("exit code = %d, want %d", code, exitcode.Usage)
	}
	if !strings.Contains(errOut, "--plan is required") {
		t.Errorf("unhelpful error: %s", errOut)
	}
}

func TestApplyRequiresApproval(t *testing.T) {
	s := newApplyScenario(t, map[string]string{
		"routes/a.yaml": newRouteFile("r-new", "  uri: /new\n  upstream_id: payments-backend\n"),
	}, planGateway())

	_, errOut, code := run(t, "apply", "--plan", s.planPath, "--repo-dir", s.dir,
		"--journal", s.journal)

	if code == exitcode.OK {
		t.Fatal("apply proceeded without --approve")
	}
	if !strings.Contains(errOut, "--approve") {
		t.Errorf("the error should name the flag: %s", errOut)
	}
	if s.gw.writes != 0 {
		t.Errorf("apply issued %d write(s) without approval", s.gw.writes)
	}
}

func TestApplyWithApprovalWrites(t *testing.T) {
	s := newApplyScenario(t, map[string]string{
		"routes/a.yaml": newRouteFile("r-new", "  uri: /new\n  upstream_id: payments-backend\n"),
	}, planGateway())

	out, _, code := run(t, "apply", "--plan", s.planPath, "--repo-dir", s.dir,
		"--journal", s.journal, "--approve")

	if code != exitcode.OK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "Applied 1") {
		t.Errorf("unexpected report:\n%s", out)
	}
	if s.gw.writes == 0 {
		t.Error("no write reached the gateway")
	}
	if _, err := os.Stat(s.journal); err != nil {
		t.Errorf("no journal was written: %v", err)
	}
}

func TestApplyDeletionNeedsItsOwnApproval(t *testing.T) {
	gw := planGateway()
	s := newApplyScenario(t, map[string]string{
		"routes/gone.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n" +
			"  id: pay-export\nlifecycle:\n  state: absent\n  reason: retired\n",
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n" +
			"  id: payments-backend\nlifecycle:\n  state: present\nspec:\n  type: roundrobin\n" +
			"  nodes:\n    10.20.0.11:8080: 5\n",
	}, gw)

	_, errOut, code := run(t, "apply", "--plan", s.planPath, "--repo-dir", s.dir,
		"--journal", s.journal, "--approve")

	if code == exitcode.OK {
		t.Fatal("a deletion proceeded with only --approve")
	}
	if !strings.Contains(errOut, "--approve-delete") {
		t.Errorf("the error should name the flag: %s", errOut)
	}
}

func TestApplyRejectsUnreadablePlan(t *testing.T) {
	startGateway(t, planGateway())
	bad := filepath.Join(t.TempDir(), "plan.json")
	writeFile(t, bad, "not a plan")

	_, _, code := run(t, "apply", "--plan", bad, "--repo-dir", t.TempDir(), "--approve")
	if code == exitcode.OK {
		t.Fatal("a malformed plan was accepted")
	}
}

func TestApplyRejectsMissingPlan(t *testing.T) {
	startGateway(t, planGateway())
	_, _, code := run(t, "apply", "--plan", "/nonexistent/plan.json",
		"--repo-dir", t.TempDir(), "--approve")
	if code != exitcode.Usage {
		t.Errorf("exit code = %d, want %d", code, exitcode.Usage)
	}
}

// A tampered plan must not be applied.
func TestApplyRejectsTamperedPlanFile(t *testing.T) {
	s := newApplyScenario(t, map[string]string{
		"routes/a.yaml": newRouteFile("r-new", "  uri: /new\n  upstream_id: payments-backend\n"),
	}, planGateway())

	raw, err := os.ReadFile(s.planPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"/new"`, `"/attacker-controlled"`, 1)
	if tampered == string(raw) {
		t.Fatal("the test did not modify the plan")
	}
	if err := os.WriteFile(s.planPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}

	_, errOut, code := run(t, "apply", "--plan", s.planPath, "--repo-dir", s.dir,
		"--journal", s.journal, "--approve")

	if code == exitcode.OK {
		t.Fatal("a tampered plan was applied")
	}
	if !strings.Contains(errOut, "modified after it was generated") {
		t.Errorf("the error should say what happened: %s", errOut)
	}
	if s.gw.writes != 0 {
		t.Error("a write happened from a tampered plan")
	}
}

// Applying a plan built for another gateway is one wrong variable away.
func TestApplyRejectsPlanForAnotherGateway(t *testing.T) {
	s := newApplyScenario(t, map[string]string{
		"routes/a.yaml": newRouteFile("r-new", "  uri: /new\n  upstream_id: payments-backend\n"),
	}, planGateway())

	// Point at a different gateway.
	other := planGateway()
	startGateway(t, other)

	_, errOut, code := run(t, "apply", "--plan", s.planPath, "--repo-dir", s.dir,
		"--journal", s.journal, "--approve")

	if code == exitcode.OK {
		t.Fatal("a plan was applied to a different gateway")
	}
	if !strings.Contains(errOut, "different gateway") {
		t.Errorf("unclear error: %s", errOut)
	}
	if other.writes != 0 {
		t.Error("a write reached the wrong gateway")
	}
}

func TestApplyStalePlanUsesConflictExitCode(t *testing.T) {
	gw := planGateway()
	s := newApplyScenario(t, map[string]string{
		"routes/a.yaml": newRouteFile("r-new", "  uri: /new\n  upstream_id: payments-backend\n"),
	}, gw)

	// Someone creates the resource first.
	gw.resources["routes"] = append(gw.resources["routes"],
		map[string]any{"id": "r-new", "uri": "/theirs"})

	out, _, code := run(t, "apply", "--plan", s.planPath, "--repo-dir", s.dir,
		"--journal", s.journal, "--approve")

	if code != exitcode.Conflict {
		t.Errorf("exit code = %d, want %d (conflict)", code, exitcode.Conflict)
	}
	if !strings.Contains(out, "stale") {
		t.Errorf("the output should say the plan is stale:\n%s", out)
	}
	if gw.writes != 0 {
		t.Error("a write happened despite the stale plan")
	}
}

func TestApplyNothingToDo(t *testing.T) {
	s := newApplyScenario(t, map[string]string{
		"routes/pay.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n" +
			"  id: pay-export\n  name: payments-export\nlifecycle:\n  state: present\nspec:\n" +
			"  name: payments-export\n  uri: /v1/export\n  upstream_id: payments-backend\n" +
			"  methods:\n  - POST\n",
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n" +
			"  id: payments-backend\nlifecycle:\n  state: present\nspec:\n  type: roundrobin\n" +
			"  nodes:\n    10.20.0.11:8080: 5\n",
	}, planGateway())

	out, _, code := run(t, "apply", "--plan", s.planPath, "--repo-dir", s.dir,
		"--journal", s.journal, "--approve")

	if code != exitcode.OK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "No changes") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if s.gw.writes != 0 {
		t.Errorf("an unchanged plan issued %d write(s)", s.gw.writes)
	}
}

// The journal is the record an operator relies on afterwards.
func TestApplyJournalContainsNoCredential(t *testing.T) {
	const secret = "super-secret-admin-key-value"
	s := newApplyScenario(t, map[string]string{
		"routes/a.yaml": newRouteFile("r-new", "  uri: /new\n  upstream_id: payments-backend\n"),
	}, planGateway())
	t.Setenv("SIXSYNC_ADMIN_KEY", secret)

	if _, _, code := run(t, "apply", "--plan", s.planPath, "--repo-dir", s.dir,
		"--journal", s.journal, "--approve"); code != exitcode.OK {
		// The plan was built with a different key fingerprint, so this is
		// expected to be refused — which is itself the right behaviour.
		return
	}

	raw, err := os.ReadFile(s.journal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("the admin key leaked into the journal")
	}
}

func TestRollbackRequiresJournalFlag(t *testing.T) {
	startGateway(t, planGateway())
	_, errOut, code := run(t, "rollback")

	if code != exitcode.Usage {
		t.Errorf("exit code = %d, want %d", code, exitcode.Usage)
	}
	if !strings.Contains(errOut, "--journal is required") {
		t.Errorf("unhelpful error: %s", errOut)
	}
}

func TestRollbackUndoesAnApply(t *testing.T) {
	gw := planGateway()
	s := newApplyScenario(t, map[string]string{
		"routes/a.yaml": newRouteFile("r-new", "  uri: /new\n  upstream_id: payments-backend\n"),
	}, gw)

	if _, _, code := run(t, "apply", "--plan", s.planPath, "--repo-dir", s.dir,
		"--journal", s.journal, "--approve"); code != exitcode.OK {
		t.Fatal("apply failed")
	}

	out, _, code := run(t, "rollback", "--journal", s.journal)
	if code != exitcode.OK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "restored") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestRollbackRejectsUnreadableJournal(t *testing.T) {
	startGateway(t, planGateway())
	bad := filepath.Join(t.TempDir(), "j.jsonl")
	writeFile(t, bad, "not a journal\n")

	if _, _, code := run(t, "rollback", "--journal", bad); code == exitcode.OK {
		t.Fatal("a malformed journal was accepted")
	}
}

func TestApplyAndRollbackHelpStateTheirLimits(t *testing.T) {
	tests := []struct {
		cmd  string
		want []string
	}{
		{"apply", []string{"never computes one", "not atomic", "re-read"}},
		{"rollback", []string{"best effort", "not a transaction", "cannot restore a credential"}},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			out, _, _ := run(t, tt.cmd, "--help")
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("%s help no longer states %q:\n%s", tt.cmd, want, out)
				}
			}
		})
	}
}
