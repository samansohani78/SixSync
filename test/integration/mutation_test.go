//go:build integration

// Mutation tests against a real APISIX.
//
// Every test here writes to the gateway, so it must only ever run against the
// disposable stack in deploy/compose. Resources are namespaced with an
// "itest-" prefix and cleaned up, so a failure does not poison later runs.
package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/apply"
	"github.com/samansohani78/SixSync/internal/journal"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/plan"
	"github.com/samansohani78/SixSync/internal/policy"
	"github.com/samansohani78/SixSync/internal/resource"
)

// mutationScenario wires a repository and a live gateway together.
type mutationScenario struct {
	t       *testing.T
	repo    *resource.Repo
	client  *apisix.Client
	journal string
	plan    *plan.Plan
}

func newMutationScenario(t *testing.T, files map[string]string) *mutationScenario {
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

	return &mutationScenario{
		t: t, repo: repo, client: adminClient(t),
		journal: filepath.Join(t.TempDir(), "journal.jsonl"),
	}
}

func (s *mutationScenario) buildPlan() *plan.Plan {
	s.t.Helper()
	p, err := plan.Build(context.Background(), plan.Options{
		Repo: s.repo, Client: s.client, Profile: policy.DefaultProfile(),
		Target: plan.Target{AdminURL: s.client.BaseURL(), KeyFingerprint: "sha256:itest"},
	})
	if err != nil {
		s.t.Fatalf("plan.Build: %v", err)
	}
	s.plan = p
	return p
}

func (s *mutationScenario) apply() (*apply.Report, error) {
	s.t.Helper()
	if s.plan == nil {
		s.buildPlan()
	}
	return apply.Run(context.Background(), apply.Options{
		Plan: s.plan, Repo: s.repo, Gateway: s.client,
		JournalPath: s.journal, Approved: true, DeleteApproved: true,
		KeyFingerprint: "sha256:itest",
		Now:            func() time.Time { return time.Now().UTC() },
	})
}

// cleanup removes test resources regardless of how a test ended.
func cleanupResources(t *testing.T, ids map[model.Kind][]string) {
	t.Helper()
	t.Cleanup(func() {
		// Routes first: an upstream cannot be removed while one references it.
		for _, kind := range []model.Kind{model.KindRoute, model.KindService, model.KindUpstream} {
			for _, id := range ids[kind] {
				def, ok := model.Lookup(kind)
				if !ok {
					continue
				}
				rawAdmin(t, "DELETE", "/apisix/admin/"+def.APIPath+"/"+id, "")
			}
		}
	})
}

func itestRoute(id, spec string) string {
	return "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: " + id +
		"\nlifecycle:\n  state: present\nspec:\n" + spec
}

func TestLiveApplyCreatesAndVerifies(t *testing.T) {
	cleanupResources(t, map[model.Kind][]string{model.KindRoute: {"itest-create"}})

	s := newMutationScenario(t, map[string]string{
		"routes/a.yaml": itestRoute("itest-create",
			"  uri: /itest-create\n  upstream_id: payments-backend\n"),
	})

	report, err := s.apply()
	if err != nil {
		t.Fatalf("apply on APISIX %s: %v (%s)", testVersion(t), err, report.Message)
	}
	if report.Applied != 1 {
		t.Fatalf("report = %+v", report)
	}

	def, _ := model.Lookup(model.KindRoute)
	got, err := s.client.Get(context.Background(), def, "itest-create")
	if err != nil {
		t.Fatalf("the resource was not created: %v", err)
	}
	if got.Value["uri"] != "/itest-create" {
		t.Errorf("uri = %v", got.Value["uri"])
	}
}

func TestLiveApplyUpdatesAndDeletes(t *testing.T) {
	cleanupResources(t, map[model.Kind][]string{model.KindRoute: {"itest-update"}})

	// Create.
	s := newMutationScenario(t, map[string]string{
		"routes/a.yaml": itestRoute("itest-update",
			"  uri: /itest-v1\n  upstream_id: payments-backend\n"),
	})
	if _, err := s.apply(); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Update.
	s2 := newMutationScenario(t, map[string]string{
		"routes/a.yaml": itestRoute("itest-update",
			"  uri: /itest-v2\n  upstream_id: payments-backend\n"),
	})
	if _, err := s2.apply(); err != nil {
		t.Fatalf("update: %v", err)
	}

	def, _ := model.Lookup(model.KindRoute)
	got, err := s2.client.Get(context.Background(), def, "itest-update")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value["uri"] != "/itest-v2" {
		t.Errorf("uri = %v, want /itest-v2", got.Value["uri"])
	}

	// Delete.
	s3 := newMutationScenario(t, map[string]string{
		"routes/a.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n" +
			"  id: itest-update\nlifecycle:\n  state: absent\n  reason: integration test\n",
	})
	if _, err := s3.apply(); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s3.client.Get(context.Background(), def, "itest-update"); err == nil {
		t.Error("the resource still exists after a delete")
	}
}

// The safety property that matters most, against a real gateway.
func TestLiveStalePlanIsRefused(t *testing.T) {
	cleanupResources(t, map[model.Kind][]string{model.KindRoute: {"itest-stale"}})

	// Seed a resource so the plan is an update.
	if code, _, b := rawAdmin(t, "PUT", "/apisix/admin/routes/itest-stale",
		`{"uri":"/itest-stale-original","upstream_id":"payments-backend"}`); code > 299 {
		t.Fatalf("seed: HTTP %d: %s", code, b)
	}

	s := newMutationScenario(t, map[string]string{
		"routes/a.yaml": itestRoute("itest-stale",
			"  uri: /itest-stale-planned\n  upstream_id: payments-backend\n"),
	})
	s.buildPlan()

	// Someone else writes between plan and apply.
	if code, _, b := rawAdmin(t, "PUT", "/apisix/admin/routes/itest-stale",
		`{"uri":"/itest-stale-someone-else","upstream_id":"payments-backend"}`); code > 299 {
		t.Fatalf("concurrent write: HTTP %d: %s", code, b)
	}

	report, err := s.apply()
	if err != nil {
		t.Fatalf("a stale plan should be reported, not error: %v", err)
	}
	if report.Outcome != apply.OutcomeStale {
		t.Fatalf("outcome = %s, want stale on APISIX %s", report.Outcome, testVersion(t))
	}

	// The concurrent change must be intact.
	def, _ := model.Lookup(model.KindRoute)
	got, err := s.client.Get(context.Background(), def, "itest-stale")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value["uri"] != "/itest-stale-someone-else" {
		t.Errorf("the concurrent change was overwritten: uri = %v", got.Value["uri"])
	}
}

func TestLiveRollbackRemovesCreatedResource(t *testing.T) {
	cleanupResources(t, map[model.Kind][]string{model.KindRoute: {"itest-rollback"}})

	s := newMutationScenario(t, map[string]string{
		"routes/a.yaml": itestRoute("itest-rollback",
			"  uri: /itest-rollback\n  upstream_id: payments-backend\n"),
	})
	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}

	j, err := journal.Open(s.journal)
	if err != nil {
		t.Fatal(err)
	}
	report, err := apply.Rollback(context.Background(), apply.RollbackOptions{
		Journal: j, Gateway: s.client,
	})
	if err != nil {
		t.Fatalf("rollback: %v (%s)", err, report.Message)
	}

	def, _ := model.Lookup(model.KindRoute)
	if _, err := s.client.Get(context.Background(), def, "itest-rollback"); err == nil {
		t.Error("the created resource survived rollback")
	}
}

func TestLiveRollbackRestoresUpdatedResource(t *testing.T) {
	cleanupResources(t, map[model.Kind][]string{model.KindRoute: {"itest-restore"}})

	if code, _, b := rawAdmin(t, "PUT", "/apisix/admin/routes/itest-restore",
		`{"uri":"/itest-original","upstream_id":"payments-backend"}`); code > 299 {
		t.Fatalf("seed: HTTP %d: %s", code, b)
	}

	s := newMutationScenario(t, map[string]string{
		"routes/a.yaml": itestRoute("itest-restore",
			"  uri: /itest-changed\n  upstream_id: payments-backend\n"),
	})
	if _, err := s.apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}

	j, err := journal.Open(s.journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apply.Rollback(context.Background(), apply.RollbackOptions{
		Journal: j, Gateway: s.client,
	}); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	def, _ := model.Lookup(model.KindRoute)
	got, err := s.client.Get(context.Background(), def, "itest-restore")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value["uri"] != "/itest-original" {
		t.Errorf("uri = %v, want the restored /itest-original", got.Value["uri"])
	}
}

// APISIX refusing a delete because something references the resource is
// information the operator should see plainly.
func TestLiveDeleteRefusedByReferenceIsReported(t *testing.T) {
	cleanupResources(t, map[model.Kind][]string{
		model.KindRoute:    {"itest-referrer"},
		model.KindUpstream: {"itest-referenced"},
	})

	if code, _, b := rawAdmin(t, "PUT", "/apisix/admin/upstreams/itest-referenced",
		`{"type":"roundrobin","nodes":{"10.40.0.1:80":1}}`); code > 299 {
		t.Fatalf("seed upstream: HTTP %d: %s", code, b)
	}
	if code, _, b := rawAdmin(t, "PUT", "/apisix/admin/routes/itest-referrer",
		`{"uri":"/itest-referrer","upstream_id":"itest-referenced"}`); code > 299 {
		t.Fatalf("seed route: HTTP %d: %s", code, b)
	}

	// Tombstone only the upstream, leaving the route in place on the gateway.
	s := newMutationScenario(t, map[string]string{
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n" +
			"  id: itest-referenced\nlifecycle:\n  state: absent\n  reason: integration test\n",
	})

	report, err := s.apply()
	if err == nil {
		t.Fatalf("APISIX %s allowed deleting a referenced upstream", testVersion(t))
	}
	_ = report

	j, jErr := journal.Open(s.journal)
	if jErr != nil {
		t.Fatal(jErr)
	}
	if len(j.Results) == 0 {
		t.Fatal("nothing was journalled")
	}
	if !strings.Contains(j.Results[0].Error, "still references it") {
		t.Errorf("the refusal should be explained plainly: %s", j.Results[0].Error)
	}
}

// Applying an unchanged repository must issue no writes at all: an identical
// write still advances modifiedIndex on APISIX and is real churn.
func TestLiveUnchangedApplyIssuesNoWrites(t *testing.T) {
	cleanupResources(t, map[model.Kind][]string{model.KindRoute: {"itest-noop"}})

	files := map[string]string{
		"routes/a.yaml": itestRoute("itest-noop",
			"  uri: /itest-noop\n  upstream_id: payments-backend\n"),
	}
	s := newMutationScenario(t, files)
	if _, err := s.apply(); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	def, _ := model.Lookup(model.KindRoute)
	before, err := s.client.Get(context.Background(), def, "itest-noop")
	if err != nil {
		t.Fatal(err)
	}

	s2 := newMutationScenario(t, files)
	report, err := s2.apply()
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if report.Outcome != apply.OutcomeNothingToDo {
		t.Errorf("outcome = %s, want nothing-to-do", report.Outcome)
	}

	after, err := s2.client.Get(context.Background(), def, "itest-noop")
	if err != nil {
		t.Fatal(err)
	}
	if after.ModifiedIndex != before.ModifiedIndex {
		t.Errorf("the revision advanced from %d to %d on an unchanged apply; a redundant write "+
			"is real churn in etcd", before.ModifiedIndex, after.ModifiedIndex)
	}
}
