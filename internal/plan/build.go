package plan

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/depgraph"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/policy"
	"github.com/samansohani78/SixSync/internal/redact"
	"github.com/samansohani78/SixSync/internal/resource"
)

// Reader is the subset of the Admin API client a plan needs.
//
// Narrow on purpose: planning is a read-only operation, and a type that cannot
// express a write cannot accidentally perform one.
type Reader interface {
	List(ctx context.Context, d model.Definition) ([]apisix.Resource, error)
	BaseURL() string
}

// Options configures plan construction.
type Options struct {
	// Repo is the loaded desired state.
	Repo *resource.Repo
	// Client reads the observed state.
	Client Reader
	// Target identifies the gateway, without credentials.
	Target Target
	// Profile is the policy profile to evaluate. Nil uses the default.
	Profile *policy.Profile
	// DeleteApproved reports whether deletion approval was supplied, which
	// some policy profiles require.
	DeleteApproved bool
}

// Build produces a plan by comparing desired state with observed state.
//
// It never writes. Every failure that would make an apply unsafe is recorded as
// a blocked operation rather than omitted, so a plan is either applicable in
// full or visibly not applicable at all.
func Build(ctx context.Context, opts Options) (*Plan, error) {
	if opts.Repo == nil {
		return nil, fmt.Errorf("no repository was loaded")
	}
	if opts.Client == nil {
		return nil, fmt.Errorf("no Admin API client was provided")
	}
	if len(opts.Repo.Errors) > 0 {
		// Planning against a repository that cannot be fully read would
		// produce a plan that silently omits whatever failed to parse.
		return nil, fmt.Errorf("%d resource file(s) could not be read; "+
			"run `sixsync lint` for details", len(opts.Repo.Errors))
	}

	observed, err := readObserved(ctx, opts.Client)
	if err != nil {
		return nil, err
	}

	desired, err := readDesired(opts.Repo)
	if err != nil {
		return nil, err
	}

	content := Content{
		FormatVersion: FormatVersion,
		Target:        opts.Target,
	}

	if content.DesiredFingerprint, err = fingerprintDesired(desired); err != nil {
		return nil, err
	}
	if content.ObservedFingerprint, err = fingerprintObserved(observed); err != nil {
		return nil, err
	}

	ops, err := buildOperations(desired, observed)
	if err != nil {
		return nil, err
	}

	graph := buildGraph(desired, observed)
	annotateReferences(ops, desired, observed, graph)
	annotateImpact(ops, graph)

	if err := orderOperations(ops, graph); err != nil {
		return nil, err
	}

	content.Operations = ops
	content.Unmanaged = findUnmanaged(desired, observed)

	content.Policy = policy.Evaluate(opts.Profile, policy.Input{
		Changes:        policyChanges(ops, desired),
		DeleteApproved: opts.DeleteApproved,
	})

	p := &Plan{Content: content}
	if err := p.Seal(); err != nil {
		return nil, err
	}
	return p, nil
}

// desiredResource is one resource the repository declares.
type desiredResource struct {
	Kind    model.Kind
	ID      string
	Path    string
	Absent  bool
	Spec    map[string]any
	Canon   map[string]any
	Digest  string
	Labels  map[string]string
	Blocked string
}

func key(kind model.Kind, id string) string { return string(kind) + "/" + id }

// relativePath renders a source path relative to the repository root.
//
// The plan's hashed content must not depend on where the repository happens to
// be checked out, or the same desired state would produce a different digest on
// a developer's machine and on a CI runner. Separators are normalized so a plan
// generated on Windows matches one generated on Linux.
func relativePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// readDesired canonicalizes every declared resource once, so the rest of the
// build compares prepared values rather than re-deriving them.
func readDesired(repo *resource.Repo) (map[string]*desiredResource, error) {
	out := make(map[string]*desiredResource, len(repo.Resources))

	for _, l := range repo.Resources {
		def, ok := model.Lookup(l.File.Kind)
		if !ok {
			return nil, fmt.Errorf("%s: unknown kind %q", l.Path, l.File.Kind)
		}

		d := &desiredResource{
			Kind:   l.File.Kind,
			ID:     l.File.Metadata.ID,
			Path:   relativePath(repo.Root, l.Path),
			Absent: l.File.Lifecycle.State == resource.StateAbsent,
			Spec:   l.File.Spec,
			Labels: specLabels(l.File.Spec),
		}

		// A refused kind must fail the plan rather than be skipped: silently
		// dropping it would let an operator believe it was managed.
		if !def.Supported {
			d.Blocked = fmt.Sprintf("kind %s is not supported: %s", def.Kind, def.RefusalReason)
			out[key(d.Kind, d.ID)] = d
			continue
		}

		if !d.Absent {
			canon, err := canonical.Canonicalize(l.File.Kind, l.File.Spec)
			if err != nil {
				// Canonicalization failing means SixSync cannot compare this
				// resource reliably, so it must not be applied on a guess.
				d.Blocked = fmt.Sprintf("cannot be compared safely: %v", err)
				out[key(d.Kind, d.ID)] = d
				continue
			}
			d.Canon = canon
			if d.Digest, err = canonical.Digest(canon); err != nil {
				return nil, fmt.Errorf("%s: %w", l.Path, err)
			}
		}

		out[key(d.Kind, d.ID)] = d
	}
	return out, nil
}

// observedResource is one resource currently on the gateway.
type observedResource struct {
	Kind          model.Kind
	ID            string
	Canon         map[string]any
	Digest        string
	ModifiedIndex int64
}

func readObserved(ctx context.Context, c Reader) (map[string]*observedResource, error) {
	out := map[string]*observedResource{}

	for _, def := range model.Supported() {
		items, err := c.List(ctx, def)
		if err != nil {
			return nil, fmt.Errorf("reading %s from the gateway: %w", def.Kind, err)
		}
		for _, r := range items {
			id, err := r.ID(def)
			if err != nil {
				return nil, err
			}
			canon, err := canonical.Canonicalize(def.Kind, r.Value)
			if err != nil {
				return nil, fmt.Errorf("remote %s/%s cannot be canonicalized: %w", def.Kind, id, err)
			}
			digest, err := canonical.Digest(canon)
			if err != nil {
				return nil, err
			}
			out[key(def.Kind, id)] = &observedResource{
				Kind: def.Kind, ID: id, Canon: canon,
				Digest: digest, ModifiedIndex: r.ModifiedIndex,
			}
		}
	}
	return out, nil
}

// buildOperations decides what to do with each declared resource.
func buildOperations(
	desired map[string]*desiredResource,
	observed map[string]*observedResource,
) ([]Operation, error) {
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	ops := make([]Operation, 0, len(keys))
	for _, k := range keys {
		d := desired[k]
		obs := observed[k]

		op := Operation{
			Kind: d.Kind, ID: d.ID, SourcePath: d.Path,
		}

		if d.Blocked != "" {
			op.Status = StatusBlocked
			op.BlockedReason = d.Blocked
			op.Type = OpUpdate
			ops = append(ops, op)
			continue
		}

		switch {
		case d.Absent:
			op.Type = OpDelete
			if obs == nil {
				// Already gone. Recorded as unchanged so the tombstone is
				// visible without generating a request that would 404.
				op.Status = StatusUnchanged
				break
			}
			op.Status = StatusPlanned
			op.Precondition = Precondition{
				ObservedModifiedIndex: obs.ModifiedIndex,
				ObservedDigest:        obs.Digest,
			}

		case obs == nil:
			op.Type = OpCreate
			op.Status = StatusPlanned
			op.Precondition = Precondition{MustNotExist: true}
			op.Changes = redactChanges(d.Kind, canonical.DiffFields(nil, d.Canon))

		case obs.Digest == d.Digest:
			op.Type = OpUpdate
			// Applying an identical resource is not a no-op on APISIX: it
			// advances modifiedIndex and update_time. Skipping is both
			// cheaper and less disruptive to anything watching.
			op.Status = StatusUnchanged
			op.Precondition = Precondition{
				ObservedModifiedIndex: obs.ModifiedIndex,
				ObservedDigest:        obs.Digest,
			}

		default:
			op.Type = OpUpdate
			op.Status = StatusPlanned
			op.Precondition = Precondition{
				ObservedModifiedIndex: obs.ModifiedIndex,
				ObservedDigest:        obs.Digest,
			}
			op.Changes = redactChanges(d.Kind, canonical.DiffFields(obs.Canon, d.Canon))
		}

		ops = append(ops, op)
	}
	return ops, nil
}

// kindRedactor adapts the kind registry's redaction policy to the field-diff
// redactor interface.
type kindRedactor struct{ policy *redact.Policy }

func (k kindRedactor) RedactPath(path string) (string, bool) {
	if k.policy == nil {
		return "", false
	}
	// Field-diff paths carry list indices as "nodes[0].host"; the redaction
	// policy matches on segments, so indices are stripped before matching.
	clean := stripIndices(path)
	if k.policy.MatchesPath(strings.Split(clean, ".")) {
		return redact.Placeholder, true
	}
	return "", false
}

func stripIndices(path string) string {
	var b strings.Builder
	depth := 0
	for _, r := range path {
		switch r {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func redactChanges(kind model.Kind, changes []canonical.FieldChange) []canonical.FieldChange {
	return canonical.Redact(changes, kindRedactor{policy: model.RedactionPolicy(kind)})
}

// buildGraph assembles the reference graph over everything the plan knows
// about, desired and observed alike, so a reference satisfied only by an
// existing gateway resource is still resolvable.
func buildGraph(
	desired map[string]*desiredResource,
	observed map[string]*observedResource,
) *depgraph.Graph {
	g := depgraph.New()

	for _, d := range desired {
		n := depgraph.Node{Kind: d.Kind, ID: d.ID}
		g.AddNode(n)
		if d.Absent || d.Canon == nil {
			continue
		}
		refs, err := depgraph.ReferencesOf(d.Kind, d.Canon)
		if err != nil {
			continue // reported separately as a blocked operation
		}
		for _, r := range refs {
			g.AddEdge(depgraph.Edge{
				From: n, To: depgraph.Node{Kind: r.Kind, ID: r.ID}, Field: r.Field,
			})
		}
	}

	for _, o := range observed {
		n := depgraph.Node{Kind: o.Kind, ID: o.ID}
		g.AddNode(n)
		refs, err := depgraph.ReferencesOf(o.Kind, o.Canon)
		if err != nil {
			continue
		}
		for _, r := range refs {
			g.AddEdge(depgraph.Edge{
				From: n, To: depgraph.Node{Kind: r.Kind, ID: r.ID}, Field: r.Field,
			})
		}
	}

	return g
}

// annotateReferences records each operation's references and blocks any whose
// target does not exist anywhere.
//
// APISIX enforces this for some kinds but not others — a plugin config
// referenced by routes can be deleted, breaking them with a 503 on live
// traffic — so catching it before apply is work SixSync has to do.
func annotateReferences(
	ops []Operation,
	desired map[string]*desiredResource,
	observed map[string]*observedResource,
	g *depgraph.Graph,
) {
	for i := range ops {
		op := &ops[i]
		d := desired[key(op.Kind, op.ID)]
		if d == nil || d.Canon == nil {
			continue
		}

		refs, err := depgraph.ReferencesOf(op.Kind, d.Canon)
		if err != nil {
			op.Status = StatusBlocked
			op.BlockedReason = err.Error()
			continue
		}

		var dangling []string
		for _, r := range refs {
			targetKey := key(r.Kind, r.ID)
			target, inRepo := desired[targetKey]
			_, onGateway := observed[targetKey]

			// A repository tombstone is authoritative: the target is on its
			// way out, so a reference to it will dangle once this plan is
			// applied, even though the resource is still on the gateway now.
			beingDeleted := inRepo && target.Absent
			resolved := (inRepo && !target.Absent) || (onGateway && !beingDeleted)
			op.References = append(op.References, Reference{
				Field: r.Field, Kind: r.Kind, ID: r.ID, Resolved: resolved,
			})
			if !resolved {
				dangling = append(dangling, fmt.Sprintf("%s -> %s", r.Field, targetKey))
			}
		}

		if len(dangling) > 0 && op.Status == StatusPlanned {
			sort.Strings(dangling)
			op.Status = StatusBlocked
			op.BlockedReason = "references a resource that exists neither in this repository " +
				"nor on the gateway: " + strings.Join(dangling, ", ")
		}
	}
	_ = g
}

// annotateImpact records how far each change reaches.
func annotateImpact(ops []Operation, g *depgraph.Graph) {
	for i := range ops {
		op := &ops[i]
		if op.Status != StatusPlanned || op.Type == OpCreate {
			// A newly created resource has nothing referencing it yet.
			continue
		}

		impact := g.BlastRadius(depgraph.Node{Kind: op.Kind, ID: op.ID})
		if impact.Total() == 0 {
			continue
		}

		out := &Impact{Total: impact.Total()}
		for _, n := range impact.Direct {
			out.DirectlyAffected = append(out.DirectlyAffected, n.String())
		}
		for _, n := range impact.Transitive {
			out.IndirectlyAffected = append(out.IndirectlyAffected, n.String())
		}
		op.Impact = out
	}
}

// orderOperations assigns dependency-safe execution order.
//
// Creates and updates run referents-first, so a route is never written pointing
// at an upstream that does not exist yet. Deletes run in the reverse order, so a
// referent is never removed while something still points at it.
func orderOperations(ops []Operation, g *depgraph.Graph) error {
	order, err := g.TopologicalOrder()
	if err != nil {
		return fmt.Errorf("cannot order operations: %w", err)
	}

	rank := make(map[string]int, len(order))
	for i, n := range order {
		rank[n.String()] = i
	}

	// Sort by phase, then by dependency rank within the phase, then by name so
	// that unrelated resources still order deterministically.
	sort.SliceStable(ops, func(i, j int) bool {
		a, b := ops[i], ops[j]
		if pa, pb := phaseOf(a), phaseOf(b); pa != pb {
			return pa < pb
		}

		ka := key(a.Kind, a.ID)
		kb := key(b.Kind, b.ID)
		ra, rb := rank[ka], rank[kb]

		if ra != rb {
			// Deletes run in reverse dependency order.
			if phaseOf(a) == phaseDelete {
				return ra > rb
			}
			return ra < rb
		}
		return ka < kb
	})

	for i := range ops {
		ops[i].Order = i
	}
	return nil
}

const (
	phaseWrite = iota
	phaseDelete
	phaseInert
)

func phaseOf(op Operation) int {
	switch {
	case op.Status != StatusPlanned:
		return phaseInert
	case op.Type == OpDelete:
		return phaseDelete
	default:
		return phaseWrite
	}
}

// findUnmanaged lists gateway resources no file describes.
func findUnmanaged(
	desired map[string]*desiredResource,
	observed map[string]*observedResource,
) []Unmanaged {
	var out []Unmanaged
	for k, o := range observed {
		if _, declared := desired[k]; declared {
			continue
		}
		out = append(out, Unmanaged{Kind: o.Kind, ID: o.ID})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// fingerprintDesired digests the repository's canonical desired state, so a
// plan can be tied to the content that produced it.
func fingerprintDesired(desired map[string]*desiredResource) (string, error) {
	summary := map[string]any{}
	for k, d := range desired {
		if d.Absent {
			summary[k] = "absent"
			continue
		}
		summary[k] = d.Digest
	}
	return canonical.Digest(summary)
}

func fingerprintObserved(observed map[string]*observedResource) (string, error) {
	summary := map[string]any{}
	for k, o := range observed {
		summary[k] = o.Digest
	}
	return canonical.Digest(summary)
}

// policyChanges projects the plan into the narrow view policy reasons about.
func policyChanges(ops []Operation, desired map[string]*desiredResource) []policy.Change {
	var out []policy.Change
	for _, op := range ops {
		if op.Status != StatusPlanned {
			continue
		}
		c := policy.Change{
			Type: string(op.Type), Kind: string(op.Kind), ID: op.ID,
		}
		if op.Impact != nil {
			c.BlastRadius = op.Impact.Total
		}
		if d := desired[key(op.Kind, op.ID)]; d != nil {
			c.Labels = d.Labels
			c.HasEmbeddedLua = hasEmbeddedLua(d.Spec)
			c.SecretLikeFields = secretLikeFields(d.Spec)
		}
		out = append(out, c)
	}
	return out
}

func specLabels(spec map[string]any) map[string]string {
	raw, ok := spec["labels"].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func hasEmbeddedLua(spec map[string]any) bool {
	for _, field := range []string{"script", "filter_func"} {
		if v, ok := spec[field]; ok && v != nil && v != "" {
			return true
		}
	}
	return false
}

// secretLikeFields reports spec paths whose values look like credentials.
func secretLikeFields(spec map[string]any) []string {
	var out []string
	var walk func(any, []string)
	walk = func(v any, path []string) {
		switch t := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k], append(append([]string{}, path...), k))
			}
		case []any:
			for _, item := range t {
				walk(item, path)
			}
		case string:
			if redact.LooksSecret(t) {
				out = append(out, strings.Join(path, "."))
			}
		}
	}
	walk(spec, nil)
	return out
}
