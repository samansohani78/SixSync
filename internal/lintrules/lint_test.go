package lintrules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/resource"
)

// repoFrom writes the given files into a temporary directory and loads them.
func repoFrom(t *testing.T, files map[string]string) *resource.Repo {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
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
	return repo
}

func route(id, spec string) string {
	return "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: " + id +
		"\nlifecycle:\n  state: present\nspec:\n" + spec
}

func findingsFor(findings []Finding, rule string) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

func TestCleanRepositoryProducesNoFindings(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("route-a", "  uri: /a\n  upstream_id: up-1\n"),
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: up-1\n" +
			"lifecycle:\n  state: present\nspec:\n  type: roundrobin\n  nodes:\n    10.0.0.1:80: 1\n",
	})
	if got := Check(repo); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

// Two files claiming one resource means the last write wins silently, and
// which one that is depends on directory ordering.
func TestDuplicateIDIsAnError(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("same-id", "  uri: /a\n"),
		"routes/b.yaml": route("same-id", "  uri: /b\n"),
	})

	got := findingsFor(Check(repo), RuleDuplicateID)
	if len(got) != 2 {
		t.Fatalf("expected both files flagged, got %d findings", len(got))
	}
	for _, f := range got {
		if f.Severity != SeverityError {
			t.Errorf("duplicate id should be an error, got %s", f.Severity)
		}
		// The message must name the other file, or the operator has to hunt.
		if !strings.Contains(f.Message, ".yaml") {
			t.Errorf("message does not name the conflicting file: %s", f.Message)
		}
	}
}

func TestDanglingReferenceIsAWarning(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("route-a", "  uri: /a\n  upstream_id: does-not-exist\n"),
	})

	got := findingsFor(Check(repo), RuleDanglingRef)
	if len(got) != 1 {
		t.Fatalf("expected one dangling-reference finding, got %d", len(got))
	}
	// Warning, not error: during incremental adoption the target may well
	// exist in APISIX without a file describing it yet.
	if got[0].Severity != SeverityWarning {
		t.Errorf("severity = %s, want warning during incremental adoption", got[0].Severity)
	}
	if !strings.Contains(got[0].Message, "does-not-exist") {
		t.Errorf("message does not name the missing target: %s", got[0].Message)
	}
}

func TestSatisfiedReferenceIsNotFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("route-a", "  uri: /a\n  upstream_id: up-1\n"),
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: up-1\n" +
			"lifecycle:\n  state: present\nspec:\n  type: roundrobin\n  nodes:\n    10.0.0.1:80: 1\n",
	})
	if got := findingsFor(Check(repo), RuleDanglingRef); len(got) != 0 {
		t.Errorf("a satisfied reference was flagged: %+v", got)
	}
}

// A reference to a resource that is being deleted is genuinely dangling.
func TestReferenceToATombstoneIsFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("route-a", "  uri: /a\n  upstream_id: up-1\n"),
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: up-1\n" +
			"lifecycle:\n  state: absent\n  reason: decommissioned\n",
	})
	if got := findingsFor(Check(repo), RuleDanglingRef); len(got) != 1 {
		t.Errorf("a reference to a resource marked absent should be flagged, got %+v", got)
	}
}

// Anyone who can merge a change to these has remote code execution on the
// data plane.
func TestEmbeddedLuaIsFlagged(t *testing.T) {
	for _, field := range []string{"script", "filter_func"} {
		t.Run(field, func(t *testing.T) {
			repo := repoFrom(t, map[string]string{
				"routes/a.yaml": route("route-a",
					"  uri: /a\n  "+field+": \"function(vars) return true end\"\n"),
			})
			got := findingsFor(Check(repo), RuleLuaCode)
			if len(got) != 1 {
				t.Fatalf("expected spec.%s to be flagged, got %+v", field, got)
			}
			if !strings.Contains(got[0].Message, "remote code execution") {
				t.Errorf("the message should explain the risk plainly: %s", got[0].Message)
			}
		})
	}
}

func TestEphemeralRouteIsFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("route-a", "  uri: /a\n  ttl: 300\n"),
	})
	if got := findingsFor(Check(repo), RuleEphemeralRoute); len(got) != 1 {
		t.Errorf("a route with a ttl should be flagged as ephemeral, got %+v", got)
	}
}

func TestPlaintextSecretIsFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("route-a",
			"  uri: /a\n  plugins:\n    key-auth:\n      key: aGVsbG93b3JsZGhlbGxvd29ybGRoZWxsb3dvcmxk\n"),
	})
	got := findingsFor(Check(repo), RulePlaintextKey)
	if len(got) != 1 {
		t.Fatalf("expected a plaintext-secret finding, got %+v", got)
	}
	if !strings.Contains(got[0].Message, "plugins.key-auth.key") {
		t.Errorf("the message should name the field: %s", got[0].Message)
	}
	// It must suggest the remedy, not just complain.
	if !strings.Contains(got[0].Message, "$secret://") {
		t.Errorf("the message should point at APISIX secret references: %s", got[0].Message)
	}
}

// A linter whose findings get ignored is worse than none, so ordinary
// configuration must stay quiet.
func TestOrdinaryConfigurationDoesNotTriggerSecretWarnings(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("route-a",
			"  uri: /v1/api/report/payment/export\n"+
				"  host: gateway.internal.example.test\n"+
				"  plugins:\n    proxy-rewrite:\n      uri: /api/export\n"),
	})
	if got := findingsFor(Check(repo), RulePlaintextKey); len(got) != 0 {
		t.Errorf("ordinary configuration produced secret warnings: %+v", got)
	}
}

func TestTombstoneWithoutReasonIsFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: gone\n" +
			"lifecycle:\n  state: absent\n",
	})
	if got := findingsFor(Check(repo), RuleAbsentNoReason); len(got) != 1 {
		t.Errorf("a deletion with no reason should be flagged, got %+v", got)
	}
}

func TestTombstoneWithReasonIsNotFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: gone\n" +
			"lifecycle:\n  state: absent\n  reason: endpoint retired 2026-08\n",
	})
	if got := findingsFor(Check(repo), RuleAbsentNoReason); len(got) != 0 {
		t.Errorf("a documented deletion should not be flagged: %+v", got)
	}
}

func TestHasErrors(t *testing.T) {
	if HasErrors([]Finding{{Severity: SeverityWarning}}) {
		t.Error("warnings alone must not fail a build")
	}
	if !HasErrors([]Finding{{Severity: SeverityWarning}, {Severity: SeverityError}}) {
		t.Error("an error must fail a build")
	}
	if HasErrors(nil) {
		t.Error("no findings must not fail a build")
	}
}

// Output feeds CI logs and review comments; it must not reorder between runs.
func TestFindingsAreDeterministicallyOrdered(t *testing.T) {
	files := map[string]string{
		"routes/a.yaml": route("dup", "  uri: /a\n  upstream_id: missing-1\n  ttl: 60\n"),
		"routes/b.yaml": route("dup", "  uri: /b\n  upstream_id: missing-2\n"),
		"routes/c.yaml": route("c", "  uri: /c\n  script: \"return 1\"\n"),
	}

	// One repository, checked repeatedly: a fresh t.TempDir() per run would
	// vary the paths and mask what this test is actually about.
	repo := repoFrom(t, files)

	var first []string
	for run := 0; run < 20; run++ {
		var got []string
		for _, f := range Check(repo) {
			got = append(got, f.Path+"|"+f.Rule)
		}
		if run == 0 {
			first = got
			continue
		}
		if strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("finding order varied between runs:\n%v\n%v", first, got)
		}
	}
	if len(first) == 0 {
		t.Fatal("expected findings from this fixture")
	}
}

func TestUnparseableFilesAreReportedSeparately(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/good.yaml": route("good", "  uri: /a\n"),
		"routes/bad.yaml":  "kind: Route\n  this is: [broken",
	})
	if len(repo.Errors) != 1 {
		t.Errorf("expected one unreadable file, got %d", len(repo.Errors))
	}
	if len(repo.Resources) != 1 {
		t.Errorf("the readable file should still load, got %d resources", len(repo.Resources))
	}
}

// --- collision rules -------------------------------------------------------

// Two routes matching the same requests is a silent problem: APISIX picks one
// by an ordering the repository does not state.
func TestRouteCollisionIsFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /pay\n  methods: [POST]\n  upstream_id: u1\n"),
		"routes/b.yaml": route("r2", "  uri: /pay\n  methods: [POST]\n  upstream_id: u1\n"),
	})

	got := findingsFor(Check(repo), RuleRouteCollision)
	if len(got) != 2 {
		t.Fatalf("expected both routes flagged, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "r2") && !strings.Contains(got[0].Message, "r1") {
		t.Errorf("the message should name the colliding route: %s", got[0].Message)
	}
	// The remedy should be stated, not just the problem.
	if !strings.Contains(got[0].Message, "priorit") {
		t.Errorf("the message should suggest a fix: %s", got[0].Message)
	}
}

// Different priorities mean the ordering was chosen deliberately.
func TestRoutesAtDifferentPrioritiesDoNotCollide(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /pay\n  methods: [POST]\n  priority: 10\n"),
		"routes/b.yaml": route("r2", "  uri: /pay\n  methods: [POST]\n  priority: 20\n"),
	})
	if got := findingsFor(Check(repo), RuleRouteCollision); len(got) != 0 {
		t.Errorf("routes at different priorities were flagged: %+v", got)
	}
}

func TestRoutesWithDifferentMethodsDoNotCollide(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /pay\n  methods: [GET]\n"),
		"routes/b.yaml": route("r2", "  uri: /pay\n  methods: [POST]\n"),
	})
	if got := findingsFor(Check(repo), RuleRouteCollision); len(got) != 0 {
		t.Errorf("routes with disjoint methods were flagged: %+v", got)
	}
}

func TestRoutesOnDifferentHostsDoNotCollide(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /pay\n  host: a.example.test\n"),
		"routes/b.yaml": route("r2", "  uri: /pay\n  host: b.example.test\n"),
	})
	if got := findingsFor(Check(repo), RuleRouteCollision); len(got) != 0 {
		t.Errorf("routes on different hosts were flagged: %+v", got)
	}
}

// A route with no method list matches every method, which is a wider net than
// any explicit list — so the two are not the same match.
func TestAbsentMethodListIsNotTheSameAsAnExplicitOne(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /pay\n"),
		"routes/b.yaml": route("r2", "  uri: /pay\n  methods: [POST]\n"),
	})
	if got := findingsFor(Check(repo), RuleRouteCollision); len(got) != 0 {
		t.Errorf("an unrestricted route was treated as colliding with a restricted one: %+v", got)
	}
}

// Warning about a collision that does not exist is worse than staying quiet,
// so routes narrowed by something this rule cannot compare are skipped.
func TestRoutesNarrowedByVarsAreNotFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /pay\n  vars:\n  - [\"arg_v\", \"==\", \"1\"]\n"),
		"routes/b.yaml": route("r2", "  uri: /pay\n  vars:\n  - [\"arg_v\", \"==\", \"2\"]\n"),
	})
	if got := findingsFor(Check(repo), RuleRouteCollision); len(got) != 0 {
		t.Errorf("routes narrowed by vars were flagged: %+v", got)
	}
}

// A tombstoned route is on its way out and cannot collide with anything.
func TestTombstonedRoutesDoNotCollide(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("r1", "  uri: /pay\n  methods: [POST]\n"),
		"routes/b.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r2\n" +
			"lifecycle:\n  state: absent\n  reason: retired\n",
	})
	if got := findingsFor(Check(repo), RuleRouteCollision); len(got) != 0 {
		t.Errorf("a tombstoned route was flagged as colliding: %+v", got)
	}
}

// import derives file names from resources, so a collision means one would
// overwrite the other on the next import.
func TestFilenameCollisionIsAnError(t *testing.T) {
	// Two ids that slug to the same token.
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("pay-export", "  uri: /a\n"),
		"routes/b.yaml": route("pay.export", "  uri: /b\n"),
	})

	got := findingsFor(Check(repo), RuleFilenameCollision)
	if len(got) != 2 {
		t.Fatalf("expected both flagged, got %d: %+v", len(got), got)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("severity = %s; one file silently overwriting another is an error",
			got[0].Severity)
	}
	if !strings.Contains(got[0].Message, "overwrite") {
		t.Errorf("the consequence should be stated: %s", got[0].Message)
	}
}

func TestDistinctFilenamesAreNotFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("route-one", "  uri: /a\n"),
		"routes/b.yaml": route("route-two", "  uri: /b\n"),
	})
	if got := findingsFor(Check(repo), RuleFilenameCollision); len(got) != 0 {
		t.Errorf("distinct file names were flagged: %+v", got)
	}
}

// APISIX treats these as separate resources; a case-insensitive filesystem
// does not.
func TestCaseOnlyIdentifierDifferenceIsFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("PayExport", "  uri: /a\n"),
		"routes/b.yaml": route("payexport", "  uri: /b\n"),
	})

	got := findingsFor(Check(repo), RuleCaseOnlyID)
	if len(got) != 2 {
		t.Fatalf("expected both flagged, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "macOS") && !strings.Contains(got[0].Message, "Windows") {
		t.Errorf("the message should explain where this breaks: %s", got[0].Message)
	}
}

// Identical identifiers are the duplicate-id rule's business, not this one.
func TestIdenticalIdentifiersAreNotACaseCollision(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": route("same", "  uri: /a\n"),
		"routes/b.yaml": route("same", "  uri: /b\n"),
	})
	if got := findingsFor(Check(repo), RuleCaseOnlyID); len(got) != 0 {
		t.Errorf("identical ids were reported as a case collision: %+v", got)
	}
	if got := findingsFor(Check(repo), RuleDuplicateID); len(got) != 2 {
		t.Errorf("the duplicate-id rule should have caught this instead: %+v", got)
	}
}

// A resource the canonicalizer refuses cannot be diffed, so drift and plan
// would be quietly wrong about it.
func TestUncomparableResourceIsFlagged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: u1\n" +
			"lifecycle:\n  state: present\nspec:\n  nodes: \"not-a-node-list\"\n",
	})

	got := findingsFor(Check(repo), RuleUncomparable)
	if len(got) != 1 {
		t.Fatalf("expected the uncomparable resource to be flagged: %+v", Check(repo))
	}
	if got[0].Severity != SeverityError {
		t.Errorf("severity = %s, want error", got[0].Severity)
	}
	if !strings.Contains(got[0].Message, "drift") {
		t.Errorf("the consequence should be stated: %s", got[0].Message)
	}
}

func TestCollisionRulesAreDeterministic(t *testing.T) {
	files := map[string]string{}
	for _, id := range []string{"r5", "r1", "r3", "r2", "r4"} {
		files["routes/"+id+".yaml"] = route(id, "  uri: /same\n  methods: [POST]\n")
	}
	repo := repoFrom(t, files)

	var first string
	for run := 0; run < 25; run++ {
		var sb strings.Builder
		for _, f := range Check(repo) {
			sb.WriteString(f.Rule + "|" + f.ID + ";")
		}
		got := sb.String()
		if run == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("collision findings varied:\n%s\n%s", first, got)
		}
	}
}
