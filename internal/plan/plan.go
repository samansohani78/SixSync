// Package plan defines SixSync's plan artifact and how it is produced.
//
// A plan is the reviewable record of what would change. It is written by
// `sixsync plan`, read by a human or a pipeline, and later consumed by
// `sixsync apply`, which refuses to act on anything else. Separating the two
// means the change that is approved is exactly the change that is applied.
//
// Two properties make that worth anything:
//
//   - Determinism. The same desired state against the same observed state
//     produces byte-identical content. Runtime facts that legitimately vary
//     between runs — when it was generated, which SixSync build — live outside
//     the hashed content so they cannot disturb it.
//
//   - Binding. A plan records the target it was built against and the exact
//     revision of every resource it touches. Applying it elsewhere, or after
//     something else has written, is refused rather than attempted.
package plan

import (
	"fmt"
	"time"

	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/policy"
)

// FormatVersion is the plan artifact's schema version.
//
// apply refuses a plan whose format it does not implement, rather than
// interpreting unfamiliar fields optimistically.
const FormatVersion = "sixsync.plan/v1alpha1"

// OpType is the kind of change an operation makes.
type OpType string

const (
	// OpCreate adds a resource that does not exist remotely.
	OpCreate OpType = "create"
	// OpUpdate replaces a resource that exists and differs.
	OpUpdate OpType = "update"
	// OpDelete removes a resource explicitly marked absent.
	OpDelete OpType = "delete"
)

// Status classifies a resource in the plan.
type Status string

const (
	// StatusPlanned means the operation will run.
	StatusPlanned Status = "planned"
	// StatusUnchanged means the resource already matches; no request is made.
	//
	// Recorded rather than omitted so a reviewer can see the whole managed
	// set, and skipped rather than re-written because an identical write still
	// advances modifiedIndex and is real churn.
	StatusUnchanged Status = "unchanged"
	// StatusBlocked means something prevents this resource from being planned
	// safely. A plan containing any blocked entry cannot be applied.
	StatusBlocked Status = "blocked"
)

// Target identifies the APISIX instance a plan was built against.
//
// It carries no credential. The key fingerprint is a truncated hash, which lets
// apply confirm it holds the same credential the plan was built with, without
// the value existing in an artifact that gets attached to a pipeline run.
type Target struct {
	// AdminURL is the normalized endpoint.
	AdminURL string `json:"admin_url"`
	// APISIXVersion is what the endpoint reported, empty if undeterminable.
	APISIXVersion string `json:"apisix_version,omitempty"`
	// KeyFingerprint identifies the credential without disclosing it.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
}

// Precondition is what must still hold at apply time for one operation.
type Precondition struct {
	// ObservedModifiedIndex is the resource's revision when the plan was built.
	//
	// APISIX offers no conditional write, so this supports detect-then-abort
	// rather than compare-and-swap: apply re-reads and refuses if it moved.
	// Zero means the resource did not exist.
	ObservedModifiedIndex int64 `json:"observed_modified_index"`

	// ObservedDigest is the canonical digest of the observed resource, empty
	// when it did not exist. It is a second, content-based check for the case
	// where a revision is unavailable or was reused.
	ObservedDigest string `json:"observed_digest,omitempty"`

	// MustNotExist is set for a create: the resource must still be absent.
	MustNotExist bool `json:"must_not_exist,omitempty"`
}

// Reference records one resource pointing at another, for review.
type Reference struct {
	Field string     `json:"field"`
	Kind  model.Kind `json:"kind"`
	ID    string     `json:"id"`
	// Resolved reports whether the target is present, either in the repository
	// or on the gateway.
	Resolved bool `json:"resolved"`
}

// Impact is how far a change reaches.
type Impact struct {
	// DirectlyAffected are resources referencing the changed one.
	DirectlyAffected []string `json:"directly_affected,omitempty"`
	// IndirectlyAffected are reached through an intermediary.
	IndirectlyAffected []string `json:"indirectly_affected,omitempty"`
	// Total is the count of both.
	Total int `json:"total"`
}

// Operation is one planned change to one resource.
type Operation struct {
	// Order is the position in dependency-safe execution order, from 0.
	Order int `json:"order"`

	Type   OpType     `json:"type"`
	Status Status     `json:"status"`
	Kind   model.Kind `json:"kind"`
	ID     string     `json:"id"`

	// SourcePath is the repository file this came from, empty for a delete
	// planned from a tombstone that has already been removed.
	SourcePath string `json:"source_path,omitempty"`

	Precondition Precondition `json:"precondition"`

	// Changes are the field-level differences, already redacted.
	Changes []canonical.FieldChange `json:"changes,omitempty"`

	// References are this resource's outgoing references.
	References []Reference `json:"references,omitempty"`

	// Impact is what this change reaches. Populated for updates and deletes,
	// where an operator's decision depends on the blast radius.
	Impact *Impact `json:"impact,omitempty"`

	// Warnings are non-blocking concerns a reviewer should see.
	Warnings []string `json:"warnings,omitempty"`

	// BlockedReason explains why Status is blocked.
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// Unmanaged is a resource present on the gateway that no file describes.
//
// Reported so an operator can see the whole picture, and never turned into a
// delete: a resource with no file is far more often one SixSync does not manage
// than one someone wants removed.
type Unmanaged struct {
	Kind model.Kind `json:"kind"`
	ID   string     `json:"id"`
}

// Content is everything a plan asserts. It alone is hashed.
type Content struct {
	FormatVersion string `json:"format_version"`
	Target        Target `json:"target"`

	// DesiredFingerprint is a digest over the repository's canonical desired
	// state, so a plan can be matched to the commit that produced it.
	DesiredFingerprint string `json:"desired_fingerprint"`

	// ObservedFingerprint is a digest over the gateway state the plan was
	// built against, across every managed kind.
	ObservedFingerprint string `json:"observed_fingerprint"`

	Operations []Operation `json:"operations"`
	Unmanaged  []Unmanaged `json:"unmanaged,omitempty"`

	// Policy is the result of evaluating the configured policy profile.
	Policy *policy.Result `json:"policy,omitempty"`
}

// Meta is runtime provenance. It is deliberately outside Content: when a plan
// was generated and by which build are useful facts, but they change every run
// and must not disturb the digest.
type Meta struct {
	GeneratedAt    time.Time `json:"generated_at"`
	SixSyncVersion string    `json:"sixsync_version"`
	SixSyncCommit  string    `json:"sixsync_commit,omitempty"`
}

// Plan is the artifact written to disk.
type Plan struct {
	Content Content `json:"content"`
	Meta    Meta    `json:"meta"`

	// Digest is the canonical digest of Content. apply verifies it before
	// acting, so a hand-edited plan is refused rather than executed.
	Digest string `json:"digest"`
}

// ComputeDigest returns the canonical digest of a plan's content.
func ComputeDigest(c Content) (string, error) {
	// Round-tripping through the canonical JSON encoder rather than hashing Go
	// structures directly means the digest is defined by the serialized form a
	// reader can inspect, not by struct layout.
	normalized, err := toCanonicalValue(c)
	if err != nil {
		return "", fmt.Errorf("normalizing plan content: %w", err)
	}
	return canonical.Digest(normalized)
}

// Seal computes and attaches the digest.
func (p *Plan) Seal() error {
	d, err := ComputeDigest(p.Content)
	if err != nil {
		return err
	}
	p.Digest = d
	return nil
}

// Verify reports whether the recorded digest still matches the content, which
// detects a plan that was edited after it was produced.
func (p *Plan) Verify() error {
	if p.Digest == "" {
		return fmt.Errorf("plan has no digest")
	}
	got, err := ComputeDigest(p.Content)
	if err != nil {
		return err
	}
	if got != p.Digest {
		return fmt.Errorf("plan digest does not match its content: recorded %s, computed %s; "+
			"the plan was modified after it was generated", p.Digest, got)
	}
	return nil
}

// HasChanges reports whether any operation would issue a request.
func (c Content) HasChanges() bool {
	for _, op := range c.Operations {
		if op.Status == StatusPlanned {
			return true
		}
	}
	return false
}

// Blocked returns the operations that prevent this plan from being applied.
func (c Content) Blocked() []Operation {
	var out []Operation
	for _, op := range c.Operations {
		if op.Status == StatusBlocked {
			out = append(out, op)
		}
	}
	return out
}

// Counts summarizes a plan for display.
type Counts struct {
	Create    int
	Update    int
	Delete    int
	Unchanged int
	Blocked   int
}

// Summarize counts the operations by outcome.
func (c Content) Summarize() Counts {
	var n Counts
	for _, op := range c.Operations {
		switch {
		case op.Status == StatusBlocked:
			n.Blocked++
		case op.Status == StatusUnchanged:
			n.Unchanged++
		case op.Type == OpCreate:
			n.Create++
		case op.Type == OpUpdate:
			n.Update++
		case op.Type == OpDelete:
			n.Delete++
		}
	}
	return n
}
