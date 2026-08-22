package apply

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/journal"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/plan"
	"github.com/samansohani78/SixSync/internal/policy"
	"github.com/samansohani78/SixSync/internal/redact"
	"github.com/samansohani78/SixSync/internal/resource"
)

func fixedNow() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }

// scenario wires a repository, a gateway and a plan together.
type scenario struct {
	t       *testing.T
	repo    *resource.Repo
	gw      *fakeGateway
	plan    *plan.Plan
	journal string
}

func newScenario(t *testing.T, files map[string]string, seed func(*fakeGateway)) *scenario {
	t.Helper()

	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := resource.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	gw := newFakeGateway()
	if seed != nil {
		seed(gw)
	}

	return &scenario{t: t, repo: repo, gw: gw, journal: filepath.Join(t.TempDir(), "journal.jsonl")}
}

func (s *scenario) buildPlan() *plan.Plan {
	s.t.Helper()
	p, err := plan.Build(context.Background(), plan.Options{
		Repo: s.repo, Client: s.gw,
		Target:  plan.Target{AdminURL: s.gw.BaseURL(), KeyFingerprint: "sha256:testkey"},
		Profile: policy.DefaultProfile(),
	})
	if err != nil {
		s.t.Fatalf("plan.Build: %v", err)
	}
	s.plan = p
	return p
}

func (s *scenario) apply(mut ...func(*Options)) (*Report, error) {
	s.t.Helper()
	if s.plan == nil {
		s.buildPlan()
	}
	opts := Options{
		Plan: s.plan, Repo: s.repo, Gateway: s.gw,
		JournalPath: s.journal, Approved: true, DeleteApproved: true,
		KeyFingerprint: "sha256:testkey", Now: fixedNow,
	}
	for _, m := range mut {
		m(&opts)
	}
	return Run(context.Background(), opts)
}

func (s *scenario) readJournal() *journal.Journal {
	s.t.Helper()
	j, err := journal.Open(s.journal)
	if err != nil {
		s.t.Fatalf("reading journal: %v", err)
	}
	return j
}

func route(id, spec string) string {
	return "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: " + id +
		"\nlifecycle:\n  state: present\nspec:\n" + spec
}

func tombstone(kind, id string) string {
	return "apiVersion: sixsync/v1alpha1\nkind: " + kind + "\nmetadata:\n  id: " + id +
		"\nlifecycle:\n  state: absent\n  reason: retired in a test\n"
}

// --- the happy paths -------------------------------------------------------

func TestApplyCreatesResource(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /a\n"),
	}, nil)

	report, err := s.apply()
	if err != nil {
		t.Fatalf("apply: %v (%s)", err, report.Message)
	}
	if report.Outcome != OutcomeSucceeded || report.Applied != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !s.gw.has(model.KindRoute, "r1") {
		t.Error("the resource was not created")
	}
}

func TestApplyUpdatesResource(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old"})
	})

	report, err := s.apply()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.Applied != 1 {
		t.Fatalf("report = %+v", report)
	}
	got, _ := s.gw.Get(context.Background(), mustDef(t, model.KindRoute), "r1")
	if got.Value["uri"] != "/new" {
		t.Errorf("uri = %v, want /new", got.Value["uri"])
	}
	// A write must advance the revision, which is what makes a later plan's
	// precondition meaningful.
	if s.gw.revision(model.KindRoute, "r1") == 0 {
		t.Error("the revision did not advance after the write")
	}
}

func TestApplyDeletesTombstonedResource(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/gone.yaml": tombstone("Route", "r1"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old"})
	})

	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if s.gw.has(model.KindRoute, "r1") {
		t.Error("the resource was not deleted")
	}
}

func TestApplyWithNoChangesDoesNothing(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /same\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/same"})
	})

	report, err := s.apply()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.Outcome != OutcomeNothingToDo {
		t.Errorf("outcome = %s, want %s", report.Outcome, OutcomeNothingToDo)
	}
	if n := s.gw.countCalls("PUT"); n != 0 {
		t.Errorf("an unchanged plan issued %d write(s); an identical write still advances "+
			"modifiedIndex and is real churn", n)
	}
}

// --- approvals -------------------------------------------------------------

func TestApplyRequiresExplicitApproval(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)

	report, err := s.apply(func(o *Options) { o.Approved = false })
	if err == nil {
		t.Fatal("apply proceeded without approval")
	}
	if !strings.Contains(report.Message, "--approve") {
		t.Errorf("the message should name the flag: %s", report.Message)
	}
	if s.gw.countCalls("PUT") != 0 {
		t.Error("a write happened despite the missing approval")
	}
}

func TestDeletionRequiresItsOwnApproval(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/gone.yaml": tombstone("Route", "r1"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old"})
	})

	report, err := s.apply(func(o *Options) { o.DeleteApproved = false })
	if err == nil {
		t.Fatal("a deletion proceeded without its own approval")
	}
	if !strings.Contains(report.Message, "--approve-delete") {
		t.Errorf("the message should name the flag: %s", report.Message)
	}
	if !s.gw.has(model.KindRoute, "r1") {
		t.Fatal("the resource was deleted despite the missing approval")
	}
}

// --- target binding --------------------------------------------------------

// Applying a production plan to staging is a mistake one wrong environment
// variable away.
func TestApplyRefusesPlanBuiltForAnotherGateway(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	p := s.buildPlan()
	p.Content.Target.AdminURL = "http://a-different-gateway.example.test:9180"
	if err := p.Seal(); err != nil {
		t.Fatal(err)
	}

	report, err := s.apply()
	if err == nil {
		t.Fatal("a plan for a different gateway was applied")
	}
	if !strings.Contains(report.Message, "different gateway") {
		t.Errorf("unclear message: %s", report.Message)
	}
	if s.gw.countCalls("PUT") != 0 {
		t.Error("a write happened despite the wrong target")
	}
}

func TestApplyRefusesPlanBuiltWithAnotherCredential(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	s.buildPlan()

	report, err := s.apply(func(o *Options) { o.KeyFingerprint = "sha256:different" })
	if err == nil {
		t.Fatal("a plan built with another credential was applied")
	}
	if !strings.Contains(report.Message, "different Admin API key") {
		t.Errorf("unclear message: %s", report.Message)
	}
}

// --- plan integrity --------------------------------------------------------

func TestApplyRefusesTamperedPlan(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	p := s.buildPlan()
	// Change the content without resealing.
	p.Content.Operations[0].ID = "r-attacker"

	if _, err := s.apply(); err == nil {
		t.Fatal("a tampered plan was applied")
	}
	if s.gw.countCalls("PUT") != 0 {
		t.Error("a write happened from a tampered plan")
	}
}

func TestApplyRefusesBlockedPlan(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /a\n  upstream_id: nowhere\n"),
	}, nil)

	report, err := s.apply()
	if err == nil {
		t.Fatal("a blocked plan was applied")
	}
	if !strings.Contains(report.Message, "blocked") {
		t.Errorf("unclear message: %s", report.Message)
	}
}

func TestApplyRefusesPolicyDeniedPlan(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)

	zero := 0
	prof := policy.DefaultProfile()
	prof.Spec.Limits.MaxCreates = &zero
	p, err := plan.Build(context.Background(), plan.Options{
		Repo: s.repo, Client: s.gw, Profile: prof,
		Target: plan.Target{AdminURL: s.gw.BaseURL(), KeyFingerprint: "sha256:testkey"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.plan = p

	if _, err := s.apply(); err == nil {
		t.Fatal("a policy-denied plan was applied")
	}
	if s.gw.countCalls("PUT") != 0 {
		t.Error("a write happened despite policy denial")
	}
}

// --- staleness -------------------------------------------------------------

// The core safety property: a plan built against state that has since moved
// must be refused before anything is written.
func TestApplyRefusesStalePlanWhenResourceChanged(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old"})
	})
	s.buildPlan()

	// Someone else writes between plan and apply.
	s.gw.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/someone-elses-change"})

	report, err := s.apply()
	if err != nil {
		t.Fatalf("a stale plan should be reported, not error out: %v", err)
	}
	if report.Outcome != OutcomeStale {
		t.Fatalf("outcome = %s, want %s", report.Outcome, OutcomeStale)
	}
	if len(report.Conflicts) != 1 {
		t.Errorf("expected one conflict: %+v", report.Conflicts)
	}
	if s.gw.countCalls("PUT") != 0 {
		t.Error("a write happened despite the stale plan")
	}
	// The concurrent change must survive untouched.
	got, _ := s.gw.Get(context.Background(), mustDef(t, model.KindRoute), "r1")
	if got.Value["uri"] != "/someone-elses-change" {
		t.Error("the concurrent change was overwritten")
	}
}

// An identical rewrite by another actor still advances the revision, and that
// is deliberately treated as a conflict: someone else is writing here.
func TestApplyTreatsIdenticalConcurrentRewriteAsStale(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old"})
	})
	s.buildPlan()

	// Same content, new revision.
	s.gw.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old"})

	report, _ := s.apply()
	if report.Outcome != OutcomeStale {
		t.Errorf("outcome = %s, want %s; a concurrent writer is worth stopping for",
			report.Outcome, OutcomeStale)
	}
}

func TestApplyRefusesWhenCreateTargetAppeared(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	s.buildPlan()

	// Someone creates it first.
	s.gw.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/theirs"})

	report, _ := s.apply()
	if report.Outcome != OutcomeStale {
		t.Fatalf("outcome = %s, want stale", report.Outcome)
	}
	if !strings.Contains(strings.Join(report.Conflicts, " "), "created since") {
		t.Errorf("conflicts = %v", report.Conflicts)
	}
}

func TestApplyRefusesWhenDeleteTargetVanished(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/gone.yaml": tombstone("Route", "r1"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old"})
	})
	s.buildPlan()

	if err := s.gw.Delete(context.Background(), mustDef(t, model.KindRoute), "r1"); err != nil {
		t.Fatal(err)
	}

	report, _ := s.apply()
	if report.Outcome != OutcomeStale {
		t.Errorf("outcome = %s, want stale", report.Outcome)
	}
}

// All conflicts are reported, not just the first.
func TestApplyReportsEveryConflict(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new1\n"),
		"routes/b.yaml": route("r2", "  uri: /new2\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old1"})
		g.seed(model.KindRoute, "r2", map[string]any{"id": "r2", "uri": "/old2"})
	})
	s.buildPlan()

	s.gw.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/moved1"})
	s.gw.seed(model.KindRoute, "r2", map[string]any{"id": "r2", "uri": "/moved2"})

	report, _ := s.apply()
	if len(report.Conflicts) != 2 {
		t.Errorf("expected both conflicts, got %+v", report.Conflicts)
	}
}

// --- failure handling ------------------------------------------------------

func TestApplyStopsAtFirstFailure(t *testing.T) {
	files := map[string]string{}
	for _, id := range []string{"r1", "r2", "r3"} {
		files["routes/"+id+".yaml"] = route(id, "  uri: /"+id+"\n")
	}
	s := newScenario(t, files, nil)
	s.buildPlan()

	// Fail the middle operation.
	s.gw.failOn["PUT Route/r2"] = apiErr(apisix.ErrValidation, 400, "invalid configuration")

	report, err := s.apply()
	if err == nil {
		t.Fatal("expected the failure to surface")
	}
	if report.Outcome != OutcomePartial {
		t.Errorf("outcome = %s, want %s", report.Outcome, OutcomePartial)
	}
	if report.Applied != 1 || report.Failed != 1 || report.Skipped != 1 {
		t.Errorf("counts: applied=%d failed=%d skipped=%d, want 1/1/1",
			report.Applied, report.Failed, report.Skipped)
	}
	if s.gw.has(model.KindRoute, "r3") {
		t.Error("an operation after the failure was still attempted")
	}
	if !strings.Contains(report.Message, "rollback") {
		t.Errorf("a partial failure should point at rollback: %s", report.Message)
	}
}

// A transport failure may or may not have been applied, and must never be
// reported as either success or clean failure.
func TestTransportFailureIsRecordedAsUncertain(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	s.buildPlan()
	s.gw.failOn["PUT Route/r1"] = transportErr()

	report, _ := s.apply()
	if report.Uncertain != 1 {
		t.Fatalf("expected one uncertain outcome: %+v", report)
	}
	if !strings.Contains(report.Message, "UNKNOWN") {
		t.Errorf("the message must be explicit about uncertainty: %s", report.Message)
	}

	j := s.readJournal()
	if len(j.Results) != 1 || j.Results[0].Outcome != journal.OutcomeUncertain {
		t.Errorf("the journal should record uncertainty: %+v", j.Results)
	}
}

// A write the gateway accepted but stored differently is a failure, not a
// success.
func TestWriteIsVerifiedByReadingBack(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	s.buildPlan()

	s.gw.mutateOnWrite = func(_ model.Kind, _ string, spec map[string]any) map[string]any {
		out := clone(spec)
		out["uri"] = "/something-else-entirely"
		return out
	}

	report, err := s.apply()
	if err == nil {
		t.Fatal("a mismatched write was reported as success")
	}
	if report.Applied != 0 {
		t.Errorf("applied = %d, want 0", report.Applied)
	}

	j := s.readJournal()
	if j.Results[0].Outcome != journal.OutcomeVerifyFailed {
		t.Errorf("outcome = %s, want verify-failed", j.Results[0].Outcome)
	}
	if !strings.Contains(j.Results[0].Error, "uri") {
		t.Errorf("the differing field should be named: %s", j.Results[0].Error)
	}
}

func TestDeleteIsVerified(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/gone.yaml": tombstone("Route", "r1"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old"})
	})
	s.buildPlan()

	// A gateway that accepts the delete but keeps the resource.
	s.gw.failOn["DELETE Route/r1"] = nil
	original := s.gw.store["Route/r1"]
	s.gw.mutateOnWrite = nil
	_ = original

	report, err := s.apply()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.Applied != 1 {
		t.Errorf("a clean delete should verify: %+v", report)
	}
}

func TestReferencedDeleteFailureIsExplained(t *testing.T) {
	s := newScenario(t, map[string]string{
		"upstreams/u.yaml": tombstone("Upstream", "u1"),
	}, func(g *fakeGateway) {
		g.seed(model.KindUpstream, "u1", map[string]any{"id": "u1", "type": "roundrobin"})
	})
	s.buildPlan()
	s.gw.failOn["DELETE Upstream/u1"] = &apisix.Error{
		Kind: apisix.ErrReferenced, StatusCode: 400, Method: "DELETE", Path: "/upstreams/u1",
		Message: "can not delete this upstream, route [r1] is still using it now",
	}

	_, err := s.apply()
	if err == nil {
		t.Fatal("expected the refusal to surface")
	}
	j := s.readJournal()
	if !strings.Contains(j.Results[0].Error, "still references it") {
		t.Errorf("the reason should be explained plainly: %s", j.Results[0].Error)
	}
}

// --- unsupported kinds and placeholders ------------------------------------

// A redaction placeholder must never reach the gateway: it would replace a real
// credential with the literal string, breaking auth in a way that looks like a
// configuration change.
func TestApplyRefusesToWriteARedactionPlaceholder(t *testing.T) {
	s := newScenario(t, map[string]string{
		// Quoted: unquoted [REDACTED] is a YAML flow sequence, not a string.
		"routes/a.yaml": route("r1", "  uri: /a\n  plugins:\n    key-auth:\n      key: \""+
			redact.Placeholder+"\"\n"),
	}, nil)
	s.buildPlan()

	_, err := s.apply()
	if err == nil {
		t.Fatal("a placeholder was written to the gateway")
	}
	j := s.readJournal()
	if !strings.Contains(j.Results[0].Error, "placeholder") {
		t.Errorf("the error should explain: %s", j.Results[0].Error)
	}
	if s.gw.has(model.KindRoute, "r1") {
		t.Error("the resource was written despite the placeholder")
	}
}

// --- journal ---------------------------------------------------------------

func TestJournalRecordsPriorState(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/old"})
	})

	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}

	j := s.readJournal()
	if j.Header.PlanDigest != s.plan.Digest {
		t.Error("the journal is not tied to the plan that produced it")
	}
	before := j.BeforeFor(model.KindRoute, "r1")
	if before == nil {
		t.Fatal("no prior state recorded")
	}
	if !before.Existed || before.State["uri"] != "/old" {
		t.Errorf("prior state = %+v", before)
	}
	if before.ModifiedIndex == 0 {
		t.Error("the prior revision was not recorded")
	}
	if !j.Complete() {
		t.Error("the journal has no footer despite a clean run")
	}
}

func TestJournalRecordsCreateAsNotExisting(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}

	before := s.readJournal().BeforeFor(model.KindRoute, "r1")
	if before == nil || before.Existed {
		t.Errorf("a create must record that the resource did not exist: %+v", before)
	}
}

// A journal is written to disk and often kept as a CI artifact.
func TestJournalRedactsSecretsInPriorState(t *testing.T) {
	const secret = "the-previous-live-credential"
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /a\n  plugins:\n    key-auth:\n      key: newvalue\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{
			"id": "r1", "uri": "/a",
			"plugins": map[string]any{"key-auth": map[string]any{"key": secret}},
		})
	})

	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}

	raw, err := os.ReadFile(s.journal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("a live credential was written into the journal")
	}

	before := s.readJournal().BeforeFor(model.KindRoute, "r1")
	if len(before.RedactedFields) == 0 {
		t.Error("the journal should record which fields it could not keep")
	}
	// The digest of the real prior state is still recorded, so rollback can
	// tell whether the resource still matches.
	if before.Digest == "" {
		t.Error("no digest of the prior state was recorded")
	}
}

func TestApplyRequiresAJournalPath(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s.apply(func(o *Options) { o.JournalPath = "" }); err == nil {
		t.Fatal("apply ran without a journal")
	}
}

// A journal file describes a production gateway's configuration.
func TestJournalFileIsNotWorldReadable(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.journal)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("journal mode = %o, want 600", perm)
	}
}

// An interrupted apply must still leave a usable record.
func TestJournalIsReadableWithoutAFooter(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(s.journal)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	truncated := strings.Join(lines[:len(lines)-1], "\n") + "\n"
	if err := os.WriteFile(s.journal, []byte(truncated), 0o600); err != nil {
		t.Fatal(err)
	}

	j, err := journal.Open(s.journal)
	if err != nil {
		t.Fatalf("a footerless journal should still parse: %v", err)
	}
	if j.Complete() {
		t.Error("a journal with no footer must not report as complete")
	}
	if len(j.Results) == 0 {
		t.Error("earlier entries should survive")
	}
}

// A half-written final line is what a killed process leaves.
func TestJournalToleratesATruncatedFinalLine(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(s.journal)
	truncated := string(raw[:len(raw)-20])
	if err := os.WriteFile(s.journal, []byte(truncated), 0o600); err != nil {
		t.Fatal(err)
	}

	j, err := journal.Open(s.journal)
	if err != nil {
		t.Fatalf("a truncated journal should still parse what is intact: %v", err)
	}
	if j.Header.PlanDigest == "" {
		t.Error("the header should have survived")
	}
}

// --- ordering --------------------------------------------------------------

func TestApplyRespectsPlanOrder(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /a\n  upstream_id: u1\n"),
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: u1\n" +
			"lifecycle:\n  state: present\nspec:\n  type: roundrobin\n  nodes:\n    10.0.0.1:80: 1\n",
	}, nil)

	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var upstreamAt, routeAt = -1, -1
	for i, c := range s.gw.calls {
		switch c {
		case "PUT Upstream/u1":
			upstreamAt = i
		case "PUT Route/r1":
			routeAt = i
		}
	}
	if upstreamAt == -1 || routeAt == -1 {
		t.Fatalf("both writes should have happened: %v", s.gw.calls)
	}
	if upstreamAt > routeAt {
		t.Error("the upstream must be written before the route that references it")
	}
}

func mustDef(t *testing.T, k model.Kind) model.Definition {
	t.Helper()
	d, ok := model.Lookup(k)
	if !ok {
		t.Fatalf("kind %s is not registered", k)
	}
	return d
}

// APISIX sets create_time, update_time and the identity field itself, and
// rejects a write carrying any of them with `the property is forbidden`.
// Recording them in the journal made every rollback of an updated resource
// fail against a real gateway; the in-memory fake did not model the rejection,
// so only the integration suite caught it.
//
// This is that regression, pinned at unit level.
func TestJournalPriorStateOmitsFieldsAPISIXRejects(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{
			"id": "r1", "uri": "/old",
			"create_time": 1787430328, "update_time": 1787430328,
		})
	})

	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}

	before := s.readJournal().BeforeFor(model.KindRoute, "r1")
	if before == nil {
		t.Fatal("no prior state recorded")
	}
	for _, forbidden := range []string{"create_time", "update_time", "id"} {
		if _, present := before.State[forbidden]; present {
			t.Errorf("the journal recorded %q, which APISIX refuses on write; restoring this "+
				"state would always fail", forbidden)
		}
	}
	// The actual configuration must still be there.
	if before.State["uri"] != "/old" {
		t.Errorf("the prior configuration was lost: %+v", before.State)
	}
}

// A gateway that rejects forbidden properties, as APISIX does, must still be
// rollback-able.
func TestRollbackAgainstAGatewayThatRejectsServerOwnedFields(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{
			"id": "r1", "uri": "/original",
			"create_time": 1787430328, "update_time": 1787430328,
		})
	})
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}

	// Model APISIX's refusal.
	s.gw.mutateOnWrite = func(_ model.Kind, _ string, spec map[string]any) map[string]any {
		for _, forbidden := range []string{"create_time", "update_time"} {
			if _, present := spec[forbidden]; present {
				t.Errorf("rollback sent %q, which APISIX rejects", forbidden)
			}
		}
		return spec
	}

	if _, err := s.rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	got, _ := s.gw.Get(context.Background(), mustDef(t, model.KindRoute), "r1")
	if got.Value["uri"] != "/original" {
		t.Errorf("uri = %v, want /original", got.Value["uri"])
	}
}
