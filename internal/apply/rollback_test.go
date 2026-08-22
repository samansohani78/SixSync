package apply

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/model"
)

func (s *scenario) rollback(mut ...func(*RollbackOptions)) (*RollbackReport, error) {
	s.t.Helper()
	j := s.readJournal()
	opts := RollbackOptions{
		Journal: j, Gateway: s.gw,
		JournalPath:    filepath.Join(s.t.TempDir(), "rollback.jsonl"),
		KeyFingerprint: "sha256:testkey", Now: fixedNow,
	}
	for _, m := range mut {
		m(&opts)
	}
	return Rollback(context.Background(), opts)
}

func TestRollbackRemovesCreatedResource(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !s.gw.has(model.KindRoute, "r1") {
		t.Fatal("setup: the resource should exist")
	}

	report, err := s.rollback()
	if err != nil {
		t.Fatalf("rollback: %v (%s)", err, report.Message)
	}
	if report.Outcome != RollbackSucceeded {
		t.Errorf("outcome = %s: %s", report.Outcome, report.Message)
	}
	if s.gw.has(model.KindRoute, "r1") {
		t.Error("the created resource was not removed")
	}
}

func TestRollbackRestoresUpdatedResource(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/original"})
	})
	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}

	report, err := s.rollback()
	if err != nil {
		t.Fatalf("rollback: %v (%s)", err, report.Message)
	}

	got, _ := s.gw.Get(context.Background(), mustDef(t, model.KindRoute), "r1")
	if got.Value["uri"] != "/original" {
		t.Errorf("uri = %v, want the original /original", got.Value["uri"])
	}
}

// A deleted resource whose prior state was recorded can be put back.
func TestRollbackRestoresDeletedResource(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/gone.yaml": tombstone("Route", "r1"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/was-here"})
	})
	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if s.gw.has(model.KindRoute, "r1") {
		t.Fatal("setup: the resource should be deleted")
	}

	if _, err := s.rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !s.gw.has(model.KindRoute, "r1") {
		t.Fatal("the deleted resource was not restored")
	}
	got, _ := s.gw.Get(context.Background(), mustDef(t, model.KindRoute), "r1")
	if got.Value["uri"] != "/was-here" {
		t.Errorf("uri = %v, want /was-here", got.Value["uri"])
	}
}

func TestRollbackOfNoChangesIsANoOp(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /same\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/same"})
	})
	// An unchanged plan writes no journal, so write one by applying a change
	// and then rolling back twice.
	s2 := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s2.apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.rollback(); err != nil {
		t.Fatal(err)
	}

	// Rolling back the same journal again: the created resource is already gone.
	report, err := s2.rollback()
	if err != nil {
		t.Fatalf("a repeated rollback should be safe: %v", err)
	}
	if report.Failed != 0 {
		t.Errorf("a repeated rollback reported failures: %+v", report.Items)
	}
	_ = s
}

// Restoring over someone else's later change is a second incident on top of the
// first.
func TestRollbackRefusesWhenStateMovedAfterTheApply(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/original"})
	})
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}

	// Someone edits it after the apply.
	s.gw.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/someone-elses-fix"})

	report, err := s.rollback()
	if err != nil {
		t.Fatalf("a blocked rollback should be reported, not error: %v", err)
	}
	if report.Outcome != RollbackBlocked {
		t.Fatalf("outcome = %s, want %s", report.Outcome, RollbackBlocked)
	}
	if len(report.ManualSteps) == 0 {
		t.Error("the blocked resources should be named")
	}

	got, _ := s.gw.Get(context.Background(), mustDef(t, model.KindRoute), "r1")
	if got.Value["uri"] != "/someone-elses-fix" {
		t.Error("the later change was overwritten despite the refusal")
	}
}

func TestRollbackForceOverridesTheRefusal(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /new\n"),
	}, func(g *fakeGateway) {
		g.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/original"})
	})
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}
	s.gw.seed(model.KindRoute, "r1", map[string]any{"id": "r1", "uri": "/someone-elses-fix"})

	if _, err := s.rollback(func(o *RollbackOptions) { o.Force = true }); err != nil {
		t.Fatalf("forced rollback: %v", err)
	}
	got, _ := s.gw.Get(context.Background(), mustDef(t, model.KindRoute), "r1")
	if got.Value["uri"] != "/original" {
		t.Errorf("uri = %v, want the restored /original", got.Value["uri"])
	}
}

// SixSync must not write a placeholder back into the gateway in the name of
// restoring.
func TestRollbackRefusesToRestoreRedactedSecrets(t *testing.T) {
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
		t.Fatal(err)
	}

	report, err := s.rollback()
	if err == nil {
		t.Fatal("expected the rollback to report that it cannot restore this")
	}
	if report.Outcome != RollbackManualRequired && report.Outcome != RollbackPartial {
		t.Errorf("outcome = %s", report.Outcome)
	}
	if len(report.ManualSteps) == 0 {
		t.Fatal("no manual steps were produced")
	}
	joined := strings.Join(report.ManualSteps, " ")
	if !strings.Contains(joined, "key-auth") {
		t.Errorf("the manual step should name the field: %s", joined)
	}

	// The live value must be untouched, not replaced with a placeholder.
	got, _ := s.gw.Get(context.Background(), mustDef(t, model.KindRoute), "r1")
	plugins := got.Value["plugins"].(map[string]any)
	keyAuth := plugins["key-auth"].(map[string]any)
	if keyAuth["key"] == "[REDACTED]" {
		t.Fatal("a redaction placeholder was written into the gateway")
	}
}

func TestRollbackRefusesADifferentGateway(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}

	j := s.readJournal()
	j.Header.AdminURL = "http://a-different-gateway.example.test:9180"

	_, err := Rollback(context.Background(), RollbackOptions{
		Journal: j, Gateway: s.gw, Now: fixedNow,
	})
	if err == nil {
		t.Fatal("rollback ran against a different gateway")
	}
	if !strings.Contains(err.Error(), "different gateway") {
		t.Errorf("unclear error: %v", err)
	}
}

// Undo in the opposite order to the one things were done in.
func TestRollbackRestoresInReverseOrder(t *testing.T) {
	s := newScenario(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /a\n  upstream_id: u1\n"),
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: u1\n" +
			"lifecycle:\n  state: present\nspec:\n  type: roundrobin\n  nodes:\n    10.0.0.1:80: 1\n",
	}, nil)
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}

	before := len(s.gw.calls)
	if _, err := s.rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var routeAt, upstreamAt = -1, -1
	for i, c := range s.gw.calls[before:] {
		switch c {
		case "DELETE Route/r1":
			routeAt = i
		case "DELETE Upstream/u1":
			upstreamAt = i
		}
	}
	if routeAt == -1 || upstreamAt == -1 {
		t.Fatalf("both deletions should have happened: %v", s.gw.calls[before:])
	}
	if routeAt > upstreamAt {
		t.Error("the route must be removed before the upstream it references")
	}
}

// Failure injection at every operation boundary.
func TestRollbackAfterFailureAtEachBoundary(t *testing.T) {
	ids := []string{"r1", "r2", "r3"}

	for _, failAt := range ids {
		t.Run("apply fails at "+failAt, func(t *testing.T) {
			files := map[string]string{}
			for _, id := range ids {
				files["routes/"+id+".yaml"] = route(id, "  uri: /"+id+"\n")
			}
			s := newScenario(t, files, nil)
			s.buildPlan()
			s.gw.failOn["PUT Route/"+failAt] = apiErr(apisix.ErrValidation, 400, "rejected")

			applyReport, _ := s.apply()

			// Whatever happened, rollback must leave nothing behind that the
			// apply created.
			rollbackReport, _ := s.rollback()

			for _, id := range ids {
				if s.gw.has(model.KindRoute, id) {
					t.Errorf("%s survived rollback after a failure at %s (apply: %+v, rollback: %+v)",
						id, failAt, applyReport.Outcome, rollbackReport.Outcome)
				}
			}
		})
	}
}

// An uncertain outcome may have taken effect, so rollback must consider it.
func TestRollbackConsidersUncertainOperations(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	s.buildPlan()
	s.gw.failOn["PUT Route/r1"] = transportErr()

	if _, err := s.apply(); err == nil {
		t.Fatal("expected the transport failure to surface")
	}

	// The write did not land in the fake, but rollback must still attempt to
	// undo it rather than assume it did not happen.
	report, _ := s.rollback()
	var attempted bool
	for _, item := range report.Items {
		if item.ID == "r1" {
			attempted = true
		}
	}
	if !attempted {
		t.Error("an uncertain operation was not considered for rollback")
	}
}

func TestRollbackReportsWhatItCouldNotDo(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}
	// The gateway refuses the undo.
	s.gw.failOn["DELETE Route/r1"] = apiErr(apisix.ErrServer, 500, "internal error")

	report, err := s.rollback()
	if err == nil {
		t.Fatal("expected the failure to surface")
	}
	if report.Outcome != RollbackManualRequired {
		t.Errorf("outcome = %s, want %s", report.Outcome, RollbackManualRequired)
	}
	if len(report.ManualSteps) == 0 {
		t.Fatal("no manual steps were produced")
	}
	if !strings.Contains(strings.Join(report.ManualSteps, " "), "Route/r1") {
		t.Errorf("the manual step should name the resource: %v", report.ManualSteps)
	}
}

func TestRollbackWritesItsOwnJournal(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := s.apply(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "rollback.jsonl")
	if _, err := s.rollback(func(o *RollbackOptions) { o.JournalPath = path }); err != nil {
		t.Fatal(err)
	}

	j, err := journalOpen(path)
	if err != nil {
		t.Fatalf("the rollback journal should be readable: %v", err)
	}
	if len(j.Results) == 0 {
		t.Error("the rollback journal records nothing")
	}
	if !strings.HasPrefix(j.Results[0].Type, "rollback:") {
		t.Errorf("entry type = %q, want a rollback: prefix", j.Results[0].Type)
	}
}

func TestRollbackWithoutAJournalIsRefused(t *testing.T) {
	s := newScenario(t, map[string]string{"routes/a.yaml": route("r1", "  uri: /a\n")}, nil)
	if _, err := Rollback(context.Background(), RollbackOptions{Gateway: s.gw}); err == nil {
		t.Fatal("rollback ran with no journal")
	}
}
