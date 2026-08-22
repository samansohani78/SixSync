package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/policy"
	"github.com/samansohani78/SixSync/internal/redact"
	"github.com/samansohani78/SixSync/internal/resource"
)

// fakeReader serves observed state without a gateway.
type fakeReader struct {
	// byKind maps a kind to the resource bodies the gateway holds.
	byKind map[model.Kind][]map[string]any
	// indices assigns a modifiedIndex per resource id.
	indices map[string]int64
	// listErr, when set, fails the read of that kind.
	listErr map[model.Kind]error
}

func (f *fakeReader) BaseURL() string { return "http://gateway.example.test:9180" }

func (f *fakeReader) List(_ context.Context, d model.Definition) ([]apisix.Resource, error) {
	if err, ok := f.listErr[d.Kind]; ok {
		return nil, err
	}
	var out []apisix.Resource
	for _, v := range f.byKind[d.Kind] {
		id, _ := v[d.IdentityField].(string)
		idx := int64(100)
		if f.indices != nil {
			if got, ok := f.indices[string(d.Kind)+"/"+id]; ok {
				idx = got
			}
		}
		// Round-trip through JSON so numbers arrive as json.Number, exactly as
		// they do from the real client.
		out = append(out, apisix.Resource{
			Value:         jsonRoundTrip(v),
			EtcdKey:       "/apisix/" + d.APIPath + "/" + id,
			ModifiedIndex: idx,
		})
	}
	return out, nil
}

func jsonRoundTrip(v map[string]any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		panic(err)
	}
	return out
}

// repoFrom writes files and loads them as a repository.
func repoFrom(t *testing.T, files map[string]string) *resource.Repo {
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
	return repo
}

func routeFile(id, spec string) string {
	return "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: " + id +
		"\nlifecycle:\n  state: present\nspec:\n" + spec
}

func upstreamFile(id, spec string) string {
	return "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: " + id +
		"\nlifecycle:\n  state: present\nspec:\n" + spec
}

func build(t *testing.T, repo *resource.Repo, r *fakeReader) *Plan {
	t.Helper()
	p, err := Build(context.Background(), Options{
		Repo: repo, Client: r,
		Target: Target{AdminURL: r.BaseURL(), APISIXVersion: "3.18.0"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

func opFor(p *Plan, kind model.Kind, id string) *Operation {
	for i := range p.Content.Operations {
		if p.Content.Operations[i].Kind == kind && p.Content.Operations[i].ID == id {
			return &p.Content.Operations[i]
		}
	}
	return nil
}

// --- classification --------------------------------------------------------

func TestPlanCreatesResourceAbsentFromGateway(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": routeFile("r1", "  uri: /a\n"),
	})
	p := build(t, repo, &fakeReader{})

	op := opFor(p, model.KindRoute, "r1")
	if op == nil {
		t.Fatal("no operation for the declared route")
	}
	if op.Type != OpCreate || op.Status != StatusPlanned {
		t.Errorf("got %s/%s, want create/planned", op.Type, op.Status)
	}
	if !op.Precondition.MustNotExist {
		t.Error("a create must require that the resource is still absent")
	}
}

func TestPlanUpdatesChangedResource(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": routeFile("r1", "  uri: /changed\n"),
	})
	r := &fakeReader{
		byKind:  map[model.Kind][]map[string]any{model.KindRoute: {{"id": "r1", "uri": "/original"}}},
		indices: map[string]int64{"Route/r1": 4242},
	}
	p := build(t, repo, r)

	op := opFor(p, model.KindRoute, "r1")
	if op.Type != OpUpdate || op.Status != StatusPlanned {
		t.Fatalf("got %s/%s, want update/planned", op.Type, op.Status)
	}
	if op.Precondition.ObservedModifiedIndex != 4242 {
		t.Errorf("precondition index = %d, want the observed 4242", op.Precondition.ObservedModifiedIndex)
	}
	if op.Precondition.ObservedDigest == "" {
		t.Error("an update must record the observed digest")
	}

	var sawURI bool
	for _, c := range op.Changes {
		if c.Path == "uri" && c.Old == "/original" && c.New == "/changed" {
			sawURI = true
		}
	}
	if !sawURI {
		t.Errorf("the uri change is not in the diff: %+v", op.Changes)
	}
}

// An identical write still advances modifiedIndex and update_time on APISIX, so
// an unchanged resource must not generate a request.
func TestPlanMarksIdenticalResourceUnchanged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": routeFile("r1", "  uri: /same\n"),
	})
	r := &fakeReader{
		byKind: map[model.Kind][]map[string]any{model.KindRoute: {{"id": "r1", "uri": "/same"}}},
	}
	p := build(t, repo, r)

	op := opFor(p, model.KindRoute, "r1")
	if op.Status != StatusUnchanged {
		t.Errorf("status = %s, want unchanged", op.Status)
	}
	if p.Content.HasChanges() {
		t.Error("a matching repository must produce no planned changes")
	}
}

// Server-set timestamps must not make a resource look changed.
func TestPlanIgnoresServerOwnedTimestamps(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": routeFile("r1", "  uri: /same\n"),
	})
	r := &fakeReader{byKind: map[model.Kind][]map[string]any{
		model.KindRoute: {{"id": "r1", "uri": "/same",
			"create_time": 1787430328, "update_time": 1787999999}},
	}}
	if build(t, repo, r).Content.HasChanges() {
		t.Error("timestamps produced a spurious change")
	}
}

func TestPlanDeletesOnlyExplicitTombstones(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/gone.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-gone\n" +
			"lifecycle:\n  state: absent\n  reason: retired\n",
	})
	r := &fakeReader{
		byKind: map[model.Kind][]map[string]any{model.KindRoute: {{"id": "r-gone", "uri": "/x"}}},
	}
	p := build(t, repo, r)

	op := opFor(p, model.KindRoute, "r-gone")
	if op.Type != OpDelete || op.Status != StatusPlanned {
		t.Errorf("got %s/%s, want delete/planned", op.Type, op.Status)
	}
	if op.Precondition.ObservedModifiedIndex == 0 {
		t.Error("a delete must record the observed revision")
	}
}

// The invariant from ADR-0002. A missing file is far more often a bad merge
// than an intent to remove a live route.
func TestPlanNeverDeletesByOmission(t *testing.T) {
	repo := repoFrom(t, map[string]string{}) // nothing declared
	r := &fakeReader{byKind: map[model.Kind][]map[string]any{
		model.KindRoute:    {{"id": "r1", "uri": "/a"}, {"id": "r2", "uri": "/b"}},
		model.KindUpstream: {{"id": "u1", "type": "roundrobin"}},
	}}
	p := build(t, repo, r)

	for _, op := range p.Content.Operations {
		if op.Type == OpDelete {
			t.Errorf("a delete was planned for %s/%s with no tombstone", op.Kind, op.ID)
		}
	}
	if len(p.Content.Unmanaged) != 3 {
		t.Errorf("expected 3 unmanaged resources, got %d", len(p.Content.Unmanaged))
	}
}

func TestPlanDeleteOfAlreadyAbsentResourceIsUnchanged(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/gone.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-gone\n" +
			"lifecycle:\n  state: absent\n  reason: retired\n",
	})
	p := build(t, repo, &fakeReader{})

	op := opFor(p, model.KindRoute, "r-gone")
	if op.Status != StatusUnchanged {
		t.Errorf("status = %s, want unchanged; the resource is already gone", op.Status)
	}
}

// --- blocking --------------------------------------------------------------

func TestPlanBlocksDanglingReference(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": routeFile("r1", "  uri: /a\n  upstream_id: nowhere\n"),
	})
	p := build(t, repo, &fakeReader{})

	op := opFor(p, model.KindRoute, "r1")
	if op.Status != StatusBlocked {
		t.Fatalf("status = %s, want blocked", op.Status)
	}
	if !strings.Contains(op.BlockedReason, "nowhere") {
		t.Errorf("the reason should name the missing target: %s", op.BlockedReason)
	}
	if len(p.Content.Blocked()) != 1 {
		t.Errorf("Blocked() returned %d operations", len(p.Content.Blocked()))
	}
}

// A reference satisfied by a resource already on the gateway is fine: that is
// ordinary incremental adoption.
func TestPlanAcceptsReferenceSatisfiedByGateway(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": routeFile("r1", "  uri: /a\n  upstream_id: u-existing\n"),
	})
	r := &fakeReader{byKind: map[model.Kind][]map[string]any{
		model.KindUpstream: {{"id": "u-existing", "type": "roundrobin"}},
	}}
	p := build(t, repo, r)

	op := opFor(p, model.KindRoute, "r1")
	if op.Status == StatusBlocked {
		t.Errorf("blocked despite the target existing on the gateway: %s", op.BlockedReason)
	}
	for _, ref := range op.References {
		if ref.Field == "upstream_id" && !ref.Resolved {
			t.Error("the reference should be marked resolved")
		}
	}
}

// A resource being deleted cannot satisfy someone else's reference.
func TestPlanBlocksReferenceToResourceBeingDeleted(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": routeFile("r1", "  uri: /a\n  upstream_id: u1\n"),
		"upstreams/u.yaml": "apiVersion: sixsync/v1alpha1\nkind: Upstream\nmetadata:\n  id: u1\n" +
			"lifecycle:\n  state: absent\n  reason: decommissioned\n",
	})
	r := &fakeReader{byKind: map[model.Kind][]map[string]any{
		model.KindUpstream: {{"id": "u1", "type": "roundrobin"}},
	}}
	p := build(t, repo, r)

	if op := opFor(p, model.KindRoute, "r1"); op.Status != StatusBlocked {
		t.Errorf("status = %s, want blocked; its upstream is being deleted", op.Status)
	}
}

// A refused kind must fail the plan, not be quietly skipped.
func TestPlanBlocksUnsupportedKinds(t *testing.T) {
	repo := repoFrom(t, map[string]string{})
	// The loader rejects unsupported kinds, so construct one directly to prove
	// the planner blocks rather than skips.
	repo.Resources = append(repo.Resources, resource.Loaded{
		Path: "consumers/c.yaml",
		File: &resource.File{
			APIVersion: resource.APIVersion, Kind: model.KindConsumer,
			Metadata:  resource.Metadata{ID: "partner"},
			Lifecycle: resource.Lifecycle{State: resource.StatePresent},
			Spec:      map[string]any{"username": "partner"},
		},
	})

	p := build(t, repo, &fakeReader{})
	op := opFor(p, model.KindConsumer, "partner")
	if op == nil {
		t.Fatal("the unsupported resource was silently dropped from the plan")
	}
	if op.Status != StatusBlocked {
		t.Errorf("status = %s, want blocked", op.Status)
	}
	if !strings.Contains(op.BlockedReason, "not supported") {
		t.Errorf("reason = %q", op.BlockedReason)
	}
}

// Planning against a repository that cannot be fully read would omit whatever
// failed to parse.
func TestPlanRefusesUnreadableRepository(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/broken.yaml": "kind: Route\n  this is: [broken",
	})
	_, err := Build(context.Background(), Options{Repo: repo, Client: &fakeReader{}})
	if err == nil {
		t.Fatal("expected an error for an unreadable repository")
	}
	if !strings.Contains(err.Error(), "lint") {
		t.Errorf("the error should point at lint: %v", err)
	}
}

func TestPlanPropagatesGatewayReadFailure(t *testing.T) {
	repo := repoFrom(t, map[string]string{"routes/a.yaml": routeFile("r1", "  uri: /a\n")})
	r := &fakeReader{listErr: map[model.Kind]error{model.KindRoute: fmt.Errorf("connection refused")}}

	_, err := Build(context.Background(), Options{Repo: repo, Client: r})
	if err == nil {
		t.Fatal("expected the read failure to surface")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the cause was lost: %v", err)
	}
}

// --- ordering --------------------------------------------------------------

func TestPlanOrdersReferentsBeforeReferrers(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml":    routeFile("r1", "  uri: /a\n  upstream_id: u1\n"),
		"upstreams/u.yaml": upstreamFile("u1", "  type: roundrobin\n  nodes:\n    10.0.0.1:80: 1\n"),
	})
	p := build(t, repo, &fakeReader{})

	var routeOrder, upstreamOrder = -1, -1
	for _, op := range p.Content.Operations {
		switch {
		case op.Kind == model.KindRoute && op.ID == "r1":
			routeOrder = op.Order
		case op.Kind == model.KindUpstream && op.ID == "u1":
			upstreamOrder = op.Order
		}
	}
	if upstreamOrder == -1 || routeOrder == -1 {
		t.Fatal("both operations should be present")
	}
	if upstreamOrder > routeOrder {
		t.Errorf("upstream at %d must precede its route at %d", upstreamOrder, routeOrder)
	}
}

// Deletes run in reverse dependency order, so a referent is never removed while
// something still points at it.
func TestPlanOrdersDeletesInReverse(t *testing.T) {
	tomb := func(kind, id string) string {
		return "apiVersion: sixsync/v1alpha1\nkind: " + kind + "\nmetadata:\n  id: " + id +
			"\nlifecycle:\n  state: absent\n  reason: retired\n"
	}
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml":    tomb("Route", "r1"),
		"upstreams/u.yaml": tomb("Upstream", "u1"),
	})
	r := &fakeReader{byKind: map[model.Kind][]map[string]any{
		model.KindRoute:    {{"id": "r1", "uri": "/a", "upstream_id": "u1"}},
		model.KindUpstream: {{"id": "u1", "type": "roundrobin"}},
	}}
	p := build(t, repo, r)

	routeOrder := opFor(p, model.KindRoute, "r1").Order
	upstreamOrder := opFor(p, model.KindUpstream, "u1").Order
	if routeOrder > upstreamOrder {
		t.Errorf("the route (order %d) must be deleted before its upstream (order %d)",
			routeOrder, upstreamOrder)
	}
}

func TestPlanOrdersWritesBeforeDeletes(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/new.yaml": routeFile("r-new", "  uri: /new\n"),
		"routes/old.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-old\n" +
			"lifecycle:\n  state: absent\n  reason: replaced\n",
	})
	r := &fakeReader{byKind: map[model.Kind][]map[string]any{
		model.KindRoute: {{"id": "r-old", "uri": "/old"}},
	}}
	p := build(t, repo, r)

	if opFor(p, model.KindRoute, "r-new").Order > opFor(p, model.KindRoute, "r-old").Order {
		t.Error("creates should run before deletes, so a replacement exists before the old one goes")
	}
}

// --- blast radius ----------------------------------------------------------

func TestPlanReportsBlastRadius(t *testing.T) {
	files := map[string]string{
		"plugin_configs/shared.yaml": "apiVersion: sixsync/v1alpha1\nkind: PluginConfig\n" +
			"metadata:\n  id: shared\nlifecycle:\n  state: present\nspec:\n  plugins:\n    cors: {}\n",
	}
	repo := repoFrom(t, files)

	// Three routes on the gateway reference the shared plugin config.
	var routes []map[string]any
	for _, id := range []string{"r1", "r2", "r3"} {
		routes = append(routes, map[string]any{
			"id": id, "uri": "/" + id, "plugin_config_id": "shared",
		})
	}
	r := &fakeReader{byKind: map[model.Kind][]map[string]any{
		model.KindRoute: routes,
		model.KindPluginConfig: {{"id": "shared", "plugins": map[string]any{
			"cors": map[string]any{}, "request-id": map[string]any{},
		}}},
	}}
	p := build(t, repo, r)

	op := opFor(p, model.KindPluginConfig, "shared")
	if op.Status != StatusPlanned {
		t.Fatalf("expected a planned update, got %s", op.Status)
	}
	if op.Impact == nil {
		t.Fatal("no impact recorded for a shared resource")
	}
	if op.Impact.Total != 3 {
		t.Errorf("Impact.Total = %d, want 3", op.Impact.Total)
	}
}

func TestPlanReportsNoImpactForCreates(t *testing.T) {
	repo := repoFrom(t, map[string]string{"routes/a.yaml": routeFile("r1", "  uri: /a\n")})
	p := build(t, repo, &fakeReader{})

	if op := opFor(p, model.KindRoute, "r1"); op.Impact != nil {
		t.Errorf("a newly created resource cannot affect anything: %+v", op.Impact)
	}
}

// --- determinism -----------------------------------------------------------

// The property the whole artifact depends on: the same inputs must produce the
// same digest, or a plan means nothing.
func TestPlanIsByteIdenticalAcrossRuns(t *testing.T) {
	files := map[string]string{
		"upstreams/u.yaml": upstreamFile("u1",
			"  type: roundrobin\n  nodes:\n    10.0.0.3:80: 1\n    10.0.0.1:80: 1\n    10.0.0.2:80: 1\n"),
	}
	for i := 0; i < 8; i++ {
		files[fmt.Sprintf("routes/r%d.yaml", i)] = routeFile(
			fmt.Sprintf("r%d", i), fmt.Sprintf("  uri: /r%d\n  upstream_id: u1\n", i))
	}

	var firstDigest string
	var firstJSON string
	for run := 0; run < 12; run++ {
		repo := repoFrom(t, files)
		p := build(t, repo, &fakeReader{})

		var sb strings.Builder
		if err := Write(&sb, p); err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			firstDigest, firstJSON = p.Digest, sb.String()
			continue
		}
		if p.Digest != firstDigest {
			t.Fatalf("plan digest varied between runs: %s then %s", firstDigest, p.Digest)
		}
		// The rendered content must match too, ignoring the runtime metadata.
		if stripMeta(sb.String()) != stripMeta(firstJSON) {
			t.Fatal("plan content varied between runs despite an identical digest")
		}
	}
}

func stripMeta(s string) string {
	i := strings.Index(s, `"meta"`)
	if i == -1 {
		return s
	}
	return s[:i]
}

// Runtime metadata must not disturb the digest, or every run would invalidate
// the previous plan for no reason.
func TestRuntimeMetadataDoesNotAffectDigest(t *testing.T) {
	repo := repoFrom(t, map[string]string{"routes/a.yaml": routeFile("r1", "  uri: /a\n")})
	p := build(t, repo, &fakeReader{})
	original := p.Digest

	p.Meta.GeneratedAt = p.Meta.GeneratedAt.Add(1e9)
	p.Meta.SixSyncVersion = "v99.99.99"
	p.Meta.SixSyncCommit = "deadbeef"

	if err := p.Seal(); err != nil {
		t.Fatal(err)
	}
	if p.Digest != original {
		t.Errorf("changing runtime metadata changed the digest: %s -> %s", original, p.Digest)
	}
}

// A plan is bound to the gateway it was built against.
func TestPlansForDifferentTargetsDiffer(t *testing.T) {
	repo := repoFrom(t, map[string]string{"routes/a.yaml": routeFile("r1", "  uri: /a\n")})

	a := build(t, repo, &fakeReader{})
	b, err := Build(context.Background(), Options{
		Repo:   repoFrom(t, map[string]string{"routes/a.yaml": routeFile("r1", "  uri: /a\n")}),
		Client: &fakeReader{},
		Target: Target{AdminURL: "http://other.example.test:9180", APISIXVersion: "3.18.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest == b.Digest {
		t.Error("plans for different targets produced the same digest; a plan must be target-bound")
	}
}

// --- secrets ---------------------------------------------------------------

// A plan is attached to pipeline runs and pasted into reviews.
func TestPlanRedactsSecretsInDiffs(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": routeFile("r1",
			"  uri: /a\n  plugins:\n    key-auth:\n      key: brand-new-credential\n"),
	})
	r := &fakeReader{byKind: map[model.Kind][]map[string]any{
		model.KindRoute: {{"id": "r1", "uri": "/a", "plugins": map[string]any{
			"key-auth": map[string]any{"key": "previous-credential"},
		}}},
	}}
	p := build(t, repo, r)

	var rendered strings.Builder
	if err := Write(&rendered, p); err != nil {
		t.Fatal(err)
	}
	out := rendered.String()

	for _, secret := range []string{"brand-new-credential", "previous-credential"} {
		if strings.Contains(out, secret) {
			t.Errorf("a credential leaked into the plan artifact: %s", secret)
		}
	}
	if !strings.Contains(out, redact.Placeholder) {
		t.Error("the change was dropped entirely; knowing a secret changed is useful")
	}
	if !strings.Contains(out, "plugins.key-auth.key") {
		t.Error("the redacted field path should still be visible")
	}
}

func TestPlanNeverContainsTheAPIKey(t *testing.T) {
	repo := repoFrom(t, map[string]string{"routes/a.yaml": routeFile("r1", "  uri: /a\n")})
	p, err := Build(context.Background(), Options{
		Repo: repo, Client: &fakeReader{},
		Target: Target{
			AdminURL:       "http://gateway.example.test:9180",
			KeyFingerprint: redact.Fingerprint("super-secret-admin-key"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	if err := Write(&sb, p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sb.String(), "super-secret-admin-key") {
		t.Fatal("the API key leaked into the plan artifact")
	}
	if !strings.Contains(sb.String(), "sha256:") {
		t.Error("expected a key fingerprint so apply can confirm the same credential")
	}
}

// --- policy ----------------------------------------------------------------

func TestPlanEvaluatesPolicy(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/a.yaml": routeFile("r1", "  uri: /a\n"),
		"routes/b.yaml": routeFile("r2", "  uri: /b\n"),
	})
	zero := 1
	prof := policy.DefaultProfile()
	prof.Spec.Limits.MaxCreates = &zero

	p, err := Build(context.Background(), Options{
		Repo: repo, Client: &fakeReader{}, Profile: prof,
		Target: Target{AdminURL: "http://gateway.example.test:9180"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Content.Policy == nil {
		t.Fatal("no policy result recorded")
	}
	if !p.Content.Policy.Denied() {
		t.Errorf("two creations against a limit of one should deny: %+v", p.Content.Policy)
	}
}

func TestPlanSummarize(t *testing.T) {
	repo := repoFrom(t, map[string]string{
		"routes/new.yaml":  routeFile("r-new", "  uri: /new\n"),
		"routes/same.yaml": routeFile("r-same", "  uri: /same\n"),
		"routes/chg.yaml":  routeFile("r-chg", "  uri: /changed\n"),
		"routes/del.yaml": "apiVersion: sixsync/v1alpha1\nkind: Route\nmetadata:\n  id: r-del\n" +
			"lifecycle:\n  state: absent\n  reason: retired\n",
	})
	r := &fakeReader{byKind: map[model.Kind][]map[string]any{model.KindRoute: {
		{"id": "r-same", "uri": "/same"},
		{"id": "r-chg", "uri": "/original"},
		{"id": "r-del", "uri": "/del"},
	}}}
	got := build(t, repo, r).Content.Summarize()

	want := Counts{Create: 1, Update: 1, Delete: 1, Unchanged: 1}
	if got != want {
		t.Errorf("Summarize() = %+v, want %+v", got, want)
	}
}
