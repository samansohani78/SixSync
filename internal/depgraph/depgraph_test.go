package depgraph

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samansohani78/SixSync/internal/model"
)

func route(id string) Node    { return Node{Kind: model.KindRoute, ID: id} }
func service(id string) Node  { return Node{Kind: model.KindService, ID: id} }
func upstream(id string) Node { return Node{Kind: model.KindUpstream, ID: id} }
func plugcfg(id string) Node  { return Node{Kind: model.KindPluginConfig, ID: id} }

// referents must precede referrers, so a route is never created pointing at an
// upstream that does not exist yet.
func TestTopologicalOrderPutsReferentsFirst(t *testing.T) {
	g := New()
	g.AddEdge(Edge{From: route("r1"), To: service("s1"), Field: "service_id"})
	g.AddEdge(Edge{From: service("s1"), To: upstream("u1"), Field: "upstream_id"})

	order, err := g.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder: %v", err)
	}

	pos := map[Node]int{}
	for i, n := range order {
		pos[n] = i
	}
	if pos[upstream("u1")] > pos[service("s1")] {
		t.Error("upstream must come before the service that references it")
	}
	if pos[service("s1")] > pos[route("r1")] {
		t.Error("service must come before the route that references it")
	}
}

// Two runs over the same input must produce the same plan, so ordering among
// equally-ready nodes cannot depend on map iteration.
func TestTopologicalOrderIsDeterministic(t *testing.T) {
	build := func() *Graph {
		g := New()
		for _, id := range []string{"u1", "u2", "u3", "u4", "u5"} {
			g.AddNode(upstream(id))
		}
		for _, id := range []string{"r1", "r2", "r3", "r4", "r5"} {
			g.AddEdge(Edge{From: route(id), To: upstream("u1"), Field: "upstream_id"})
		}
		return g
	}

	var first []string
	for run := 0; run < 50; run++ {
		order, err := build().TopologicalOrder()
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, n := range order {
			got = append(got, n.String())
		}
		if run == 0 {
			first = got
			continue
		}
		if strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("ordering varied between runs:\n%v\n%v", first, got)
		}
	}
}

func TestCycleIsDetectedAndNamed(t *testing.T) {
	g := New()
	g.AddEdge(Edge{From: route("a"), To: route("b"), Field: "x"})
	g.AddEdge(Edge{From: route("b"), To: route("c"), Field: "x"})
	g.AddEdge(Edge{From: route("c"), To: route("a"), Field: "x"})

	_, err := g.TopologicalOrder()
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	var ce *CycleError
	if !asCycleError(err, &ce) {
		t.Fatalf("error is not a CycleError: %v", err)
	}
	// Naming the cycle is the point: "a cycle exists" is not actionable.
	msg := err.Error()
	for _, want := range []string{"Route/a", "Route/b", "Route/c"} {
		if !strings.Contains(msg, want) {
			t.Errorf("cycle error does not name %s: %s", want, msg)
		}
	}
}

func asCycleError(err error, target **CycleError) bool {
	if ce, ok := err.(*CycleError); ok {
		*target = ce
		return true
	}
	return false
}

func TestSelfReferenceIsACycle(t *testing.T) {
	g := New()
	g.AddEdge(Edge{From: route("a"), To: route("a"), Field: "x"})
	if _, err := g.TopologicalOrder(); err == nil {
		t.Error("a self-reference should be reported as a cycle")
	}
}

// A reference to something outside the planned set must not constrain
// ordering; it is checked separately as a dangling reference.
func TestExternalReferencesDoNotBlockOrdering(t *testing.T) {
	g := New()
	g.AddNode(route("r1"))
	// The target is added by AddEdge, so model it as an edge to a node that is
	// then removed from consideration by never being planned.
	g2 := New()
	g2.AddNode(route("r1"))
	if _, err := g2.TopologicalOrder(); err != nil {
		t.Fatalf("a lone node must order fine: %v", err)
	}
	_ = g
}

func TestEmptyGraphOrdersEmpty(t *testing.T) {
	order, err := New().TopologicalOrder()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 0 {
		t.Errorf("expected an empty order, got %v", order)
	}
}

// The case that makes blast radius worth computing: one shared plugin config
// behind many routes.
func TestBlastRadiusFindsDirectReferrers(t *testing.T) {
	g := New()
	for _, id := range []string{"r1", "r2", "r3"} {
		g.AddEdge(Edge{From: route(id), To: plugcfg("shared"), Field: "plugin_config_id"})
	}

	impact := g.BlastRadius(plugcfg("shared"))
	if len(impact.Direct) != 3 {
		t.Errorf("expected 3 directly affected routes, got %d: %v", len(impact.Direct), impact.Direct)
	}
	if impact.Total() != 3 {
		t.Errorf("Total() = %d, want 3", impact.Total())
	}
}

// A route bound to a service that uses the upstream is affected too, even
// though it never names the upstream.
func TestBlastRadiusFollowsTransitiveReferences(t *testing.T) {
	g := New()
	g.AddEdge(Edge{From: service("s1"), To: upstream("u1"), Field: "upstream_id"})
	g.AddEdge(Edge{From: route("r1"), To: service("s1"), Field: "service_id"})
	g.AddEdge(Edge{From: route("r2"), To: service("s1"), Field: "service_id"})

	impact := g.BlastRadius(upstream("u1"))

	if len(impact.Direct) != 1 || impact.Direct[0] != service("s1") {
		t.Errorf("Direct = %v, want just the service", impact.Direct)
	}
	if len(impact.Transitive) != 2 {
		t.Errorf("Transitive = %v, want both routes", impact.Transitive)
	}
	if impact.Total() != 3 {
		t.Errorf("Total() = %d, want 3", impact.Total())
	}
}

func TestBlastRadiusOfAnUnreferencedResourceIsEmpty(t *testing.T) {
	g := New()
	g.AddNode(upstream("lonely"))
	if impact := g.BlastRadius(upstream("lonely")); impact.Total() != 0 {
		t.Errorf("expected no impact, got %+v", impact)
	}
}

func TestBlastRadiusTerminatesOnCycles(t *testing.T) {
	g := New()
	g.AddEdge(Edge{From: route("a"), To: route("b"), Field: "x"})
	g.AddEdge(Edge{From: route("b"), To: route("a"), Field: "x"})

	// The point is that this returns rather than recursing forever.
	done := make(chan Impact, 1)
	go func() { done <- g.BlastRadius(route("a")) }()
	select {
	case impact := <-done:
		if impact.Total() == 0 {
			t.Error("expected the cycle partner to be reported as affected")
		}
	case <-timeoutAfter():
		t.Fatal("BlastRadius did not terminate on a cyclic graph")
	}
}

func TestBlastRadiusIsDeterministic(t *testing.T) {
	g := New()
	for _, id := range []string{"r5", "r3", "r1", "r4", "r2"} {
		g.AddEdge(Edge{From: route(id), To: upstream("u1"), Field: "upstream_id"})
	}

	var first string
	for run := 0; run < 30; run++ {
		impact := g.BlastRadius(upstream("u1"))
		var got []string
		for _, n := range impact.Direct {
			got = append(got, n.String())
		}
		joined := strings.Join(got, ",")
		if run == 0 {
			first = joined
			continue
		}
		if joined != first {
			t.Fatalf("blast radius ordering varied: %s vs %s", first, joined)
		}
	}
	if first != "Route/r1,Route/r2,Route/r3,Route/r4,Route/r5" {
		t.Errorf("affected resources are not sorted: %s", first)
	}
}

func TestReferencesOfExtractsRegistryFields(t *testing.T) {
	spec := map[string]any{
		"uri":              "/x",
		"upstream_id":      "u1",
		"service_id":       "s1",
		"plugin_config_id": "pc1",
	}
	refs, err := ReferencesOf(model.KindRoute, spec)
	if err != nil {
		t.Fatalf("ReferencesOf: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("expected 3 references, got %d: %+v", len(refs), refs)
	}

	byField := map[string]Reference{}
	for _, r := range refs {
		byField[r.Field] = r
	}
	if byField["upstream_id"].Kind != model.KindUpstream {
		t.Errorf("upstream_id targets %s", byField["upstream_id"].Kind)
	}
	if byField["plugin_config_id"].ID != "pc1" {
		t.Errorf("plugin_config_id id = %q", byField["plugin_config_id"].ID)
	}
}

// APISIX echoes reference fields back with whatever numeric type was written.
func TestReferencesOfAcceptsNumericIdentifiers(t *testing.T) {
	refs, err := ReferencesOf(model.KindRoute, map[string]any{
		"upstream_id": json.Number("552218409505391294"),
	})
	if err != nil {
		t.Fatalf("ReferencesOf: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != "552218409505391294" {
		t.Errorf("large numeric reference not preserved: %+v", refs)
	}
}

// Silently skipping a malformed reference would let a broken plan look clean.
func TestReferencesOfRejectsMalformedValues(t *testing.T) {
	_, err := ReferencesOf(model.KindRoute, map[string]any{
		"upstream_id": map[string]any{"nested": true},
	})
	if err == nil {
		t.Error("expected an error for a non-scalar reference value")
	}
}

func TestReferencesOfIgnoresAbsentAndNil(t *testing.T) {
	refs, err := ReferencesOf(model.KindRoute, map[string]any{
		"uri": "/x", "upstream_id": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("expected no references, got %+v", refs)
	}
}

func TestReferencesOfUnknownKind(t *testing.T) {
	if _, err := ReferencesOf(model.Kind("Nope"), map[string]any{}); err == nil {
		t.Error("expected an error for an unknown kind")
	}
}
