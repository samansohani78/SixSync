// Package depgraph orders resource operations by their references and reports
// what a change reaches.
//
// APISIX enforces some reference integrity itself — it refuses to delete an
// upstream a route still uses — but not all of it. A plugin config referenced
// by routes can be deleted, and the routes then fail with a 503 on live
// traffic. Ordering operations correctly, and telling an operator how far a
// change reaches before they approve it, is work SixSync has to do.
package depgraph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/samansohani78/SixSync/internal/model"
)

// Node identifies one resource in the graph.
type Node struct {
	Kind model.Kind
	ID   string
}

func (n Node) String() string { return string(n.Kind) + "/" + n.ID }

// Less orders nodes deterministically.
func (n Node) Less(o Node) bool {
	if n.Kind != o.Kind {
		return n.Kind < o.Kind
	}
	return n.ID < o.ID
}

// Edge is a reference from one resource to another.
type Edge struct {
	// From is the resource holding the reference, e.g. a Route.
	From Node
	// To is the resource being referenced, e.g. an Upstream.
	To Node
	// Field is the spec field carrying the reference, e.g. "upstream_id".
	Field string
}

// Graph is a directed graph of resources and their references.
//
// An edge points from the referrer to the referent: Route → Upstream. A
// referent must therefore exist before its referrers, which is what
// TopologicalOrder produces.
type Graph struct {
	nodes map[Node]struct{}
	// out maps a node to what it references.
	out map[Node][]Edge
	// in maps a node to what references it.
	in map[Node][]Edge
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{
		nodes: make(map[Node]struct{}),
		out:   make(map[Node][]Edge),
		in:    make(map[Node][]Edge),
	}
}

// AddNode registers a resource, whether or not it participates in any edge.
func (g *Graph) AddNode(n Node) {
	g.nodes[n] = struct{}{}
}

// AddEdge records that From references To. Both endpoints are registered.
func (g *Graph) AddEdge(e Edge) {
	g.AddNode(e.From)
	g.AddNode(e.To)
	g.out[e.From] = append(g.out[e.From], e)
	g.in[e.To] = append(g.in[e.To], e)
}

// Nodes returns every node, sorted.
func (g *Graph) Nodes() []Node {
	out := make([]Node, 0, len(g.nodes))
	for n := range g.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

// Has reports whether a node is present.
func (g *Graph) Has(n Node) bool {
	_, ok := g.nodes[n]
	return ok
}

// References returns what a node points at, sorted.
func (g *Graph) References(n Node) []Edge {
	out := append([]Edge(nil), g.out[n]...)
	sortEdges(out)
	return out
}

// ReferencedBy returns what points at a node, sorted.
func (g *Graph) ReferencedBy(n Node) []Edge {
	out := append([]Edge(nil), g.in[n]...)
	sortEdges(out)
	return out
}

func sortEdges(e []Edge) {
	sort.Slice(e, func(i, j int) bool {
		if e[i].From != e[j].From {
			return e[i].From.Less(e[j].From)
		}
		if e[i].To != e[j].To {
			return e[i].To.Less(e[j].To)
		}
		return e[i].Field < e[j].Field
	})
}

// CycleError reports a reference cycle, naming the exact path so an operator
// can break it rather than being told only that one exists.
type CycleError struct {
	Cycle []Node
}

func (e *CycleError) Error() string {
	parts := make([]string, 0, len(e.Cycle))
	for _, n := range e.Cycle {
		parts = append(parts, n.String())
	}
	return "reference cycle: " + strings.Join(parts, " -> ")
}

// TopologicalOrder returns nodes ordered so that every referent precedes its
// referrers — the order in which resources can be created or updated safely.
//
// The result is deterministic: among nodes that are equally ready, the
// lowest-sorting one comes first. Without that, two runs over the same input
// could emit different plans, and a plan digest would be meaningless.
func (g *Graph) TopologicalOrder() ([]Node, error) {
	// Count each node's unresolved references.
	pending := make(map[Node]int, len(g.nodes))
	for n := range g.nodes {
		count := 0
		for _, e := range g.out[n] {
			// Only edges to nodes inside the graph constrain ordering. A
			// reference to something not being planned is checked elsewhere as
			// a dangling reference.
			//
			// A self-edge is counted deliberately: a resource referencing
			// itself can never be ordered, and reporting it as a cycle is more
			// useful than quietly tolerating invalid configuration.
			if _, ok := g.nodes[e.To]; ok {
				count++
			}
		}
		pending[n] = count
	}

	var ready []Node
	for n, c := range pending {
		if c == 0 {
			ready = append(ready, n)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].Less(ready[j]) })

	out := make([]Node, 0, len(g.nodes))
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		out = append(out, n)

		var freed []Node
		for _, e := range g.in[n] {
			if _, ok := g.nodes[e.From]; !ok {
				continue
			}
			pending[e.From]--
			if pending[e.From] == 0 {
				freed = append(freed, e.From)
			}
		}
		if len(freed) > 0 {
			ready = append(ready, freed...)
			sort.Slice(ready, func(i, j int) bool { return ready[i].Less(ready[j]) })
		}
	}

	if len(out) != len(g.nodes) {
		return nil, &CycleError{Cycle: g.findCycle(pending)}
	}
	return out, nil
}

// findCycle recovers one concrete cycle among the nodes that never became
// ready, so the error can name it.
func (g *Graph) findCycle(pending map[Node]int) []Node {
	var stuck []Node
	for n, c := range pending {
		if c > 0 {
			stuck = append(stuck, n)
		}
	}
	sort.Slice(stuck, func(i, j int) bool { return stuck[i].Less(stuck[j]) })
	if len(stuck) == 0 {
		return nil
	}

	// Walk references from the lowest-sorting stuck node until a node repeats.
	seen := map[Node]int{}
	var path []Node
	cur := stuck[0]
	for {
		if idx, ok := seen[cur]; ok {
			return append(path[idx:], cur)
		}
		seen[cur] = len(path)
		path = append(path, cur)

		next, found := Node{}, false
		for _, e := range g.References(cur) {
			if pending[e.To] > 0 || e.To == cur {
				next, found = e.To, true
				break
			}
		}
		if !found {
			return path
		}
		cur = next
	}
}

// Impact is what a change to one resource reaches.
type Impact struct {
	// Direct are resources that reference the changed one.
	Direct []Node
	// Transitive are reached through one or more intermediaries — a route
	// bound to a service that uses the changed upstream, for example.
	Transitive []Node
}

// Total is how many resources are affected in all.
func (i Impact) Total() int { return len(i.Direct) + len(i.Transitive) }

// BlastRadius reports every resource affected by changing n, following
// references backwards.
//
// This is what turns "update plugin_config/auth-standard-v1" into "this changes
// behaviour for 306 routes, at once" — the difference between a reviewable
// change and an unreviewable one.
func (g *Graph) BlastRadius(n Node) Impact {
	direct := map[Node]struct{}{}
	for _, e := range g.ReferencedBy(n) {
		if e.From != n {
			direct[e.From] = struct{}{}
		}
	}

	all := map[Node]struct{}{}
	var walk func(Node)
	walk = func(cur Node) {
		for _, e := range g.ReferencedBy(cur) {
			if e.From == n || e.From == cur {
				continue
			}
			if _, seen := all[e.From]; seen {
				continue
			}
			all[e.From] = struct{}{}
			walk(e.From)
		}
	}
	walk(n)

	var impact Impact
	for node := range all {
		if _, isDirect := direct[node]; isDirect {
			impact.Direct = append(impact.Direct, node)
		} else {
			impact.Transitive = append(impact.Transitive, node)
		}
	}
	sort.Slice(impact.Direct, func(i, j int) bool { return impact.Direct[i].Less(impact.Direct[j]) })
	sort.Slice(impact.Transitive, func(i, j int) bool {
		return impact.Transitive[i].Less(impact.Transitive[j])
	})
	return impact
}

// Build constructs a graph from resources and their reference fields.
//
// refs is called for each node and returns its outgoing references as
// (field, target kind, target id). A reference to a resource outside the given
// set still produces an edge, so that dangling references are visible.
func Build(nodes []Node, refs func(Node) []Reference) *Graph {
	g := New()
	for _, n := range nodes {
		g.AddNode(n)
	}
	for _, n := range nodes {
		for _, r := range refs(n) {
			g.AddEdge(Edge{From: n, To: Node{Kind: r.Kind, ID: r.ID}, Field: r.Field})
		}
	}
	return g
}

// Reference is one outgoing reference from a resource.
type Reference struct {
	Field string
	Kind  model.Kind
	ID    string
}

// ReferencesOf extracts the reference fields of a resource spec, using the
// kind registry as the authority on which fields are references.
//
// A reference whose value is not a string or number is reported as an error
// rather than skipped: silently ignoring a malformed reference would let a
// broken plan look clean.
func ReferencesOf(kind model.Kind, spec map[string]any) ([]Reference, error) {
	def, ok := model.Lookup(kind)
	if !ok {
		return nil, fmt.Errorf("unknown kind %q", kind)
	}

	var out []Reference
	for _, ref := range def.References {
		raw, present := spec[ref.Field]
		if !present || raw == nil {
			continue
		}
		id, err := referenceID(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", kind, ref.Field, err)
		}
		if id == "" {
			continue
		}
		out = append(out, Reference{Field: ref.Field, Kind: ref.Target, ID: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out, nil
}

func referenceID(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case fmt.Stringer:
		return t.String(), nil
	default:
		// json.Number satisfies Stringer, so anything reaching here is a shape
		// a reference field should never hold.
		return "", fmt.Errorf("expected a string or number, got %T", v)
	}
}
