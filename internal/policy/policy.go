// Package policy evaluates organizational safety rules against a planned change.
//
// The separation that matters: SixSync's own invariants — never delete by
// omission, never apply a stale plan — are not policy. They are not
// configurable, because a tool whose safety properties can be switched off does
// not have safety properties. Policy is the layer above that, where an
// organization expresses its own limits: how many resources one change may
// touch, which resources are protected, whether embedded Lua is permitted.
//
// Evaluation is deterministic and needs no network access, so the same profile
// and the same plan always produce the same verdict.
package policy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// Outcome is a rule's verdict.
type Outcome string

const (
	// OutcomeAllow means the rule was satisfied.
	OutcomeAllow Outcome = "allow"
	// OutcomeWarn means the rule was violated but the change may proceed.
	OutcomeWarn Outcome = "warn"
	// OutcomeDeny means the rule was violated and the change must not proceed.
	OutcomeDeny Outcome = "deny"
)

// Severity is how a profile wants a violation treated.
type Severity string

const (
	SeverityOff  Severity = "off"
	SeverityWarn Severity = "warn"
	SeverityDeny Severity = "deny"
)

// Rule identifiers. These appear in output and in review comments, so they are
// a public interface and must stay stable.
const (
	RuleMaxCreates          = "max-creates"
	RuleMaxUpdates          = "max-updates"
	RuleMaxDeletes          = "max-deletes"
	RuleMaxTotalChanges     = "max-total-changes"
	RuleMaxBlastRadius      = "max-blast-radius"
	RuleProtectedResources  = "protected-resources"
	RuleNoEmbeddedLua       = "no-embedded-lua"
	RuleDeleteNeedsApproval = "delete-requires-approval"
	// This is a rule identifier, not a credential, despite the name.
	RuleNoPlaintextSecrets = "no-plaintext-secrets" //nolint:gosec // G101: rule name
	RuleRequireLabels      = "require-labels"
)

// Violation is one rule firing against one subject.
type Violation struct {
	Rule    string  `json:"rule"`
	Outcome Outcome `json:"outcome"`
	// Subject is the resource the violation concerns, empty for a rule that
	// applies to the change as a whole.
	Subject string `json:"subject,omitempty"`
	Message string `json:"message"`
}

// Result is the outcome of evaluating a profile.
type Result struct {
	// ProfileName identifies which profile was applied.
	ProfileName string `json:"profile_name,omitempty"`
	// Outcome is the strongest outcome across all rules.
	Outcome Outcome `json:"outcome"`
	// Violations are every rule that fired, ordered deterministically.
	Violations []Violation `json:"violations,omitempty"`
	// Evaluated lists the rules that ran, so a reader can tell a rule that
	// passed from one that was never configured.
	Evaluated []string `json:"evaluated"`
}

// Denied reports whether the change must be blocked.
func (r *Result) Denied() bool { return r != nil && r.Outcome == OutcomeDeny }

// Limits are numeric caps. A nil field means the limit is not configured, which
// is different from a limit of zero — zero means "no changes of this kind are
// permitted at all".
type Limits struct {
	MaxCreates      *int `yaml:"max_creates" json:"max_creates,omitempty"`
	MaxUpdates      *int `yaml:"max_updates" json:"max_updates,omitempty"`
	MaxDeletes      *int `yaml:"max_deletes" json:"max_deletes,omitempty"`
	MaxTotalChanges *int `yaml:"max_total_changes" json:"max_total_changes,omitempty"`
	// MaxBlastRadius caps how many resources a single change may affect.
	MaxBlastRadius *int `yaml:"max_blast_radius" json:"max_blast_radius,omitempty"`
}

// Protected marks resources that must not be changed or removed casually.
type Protected struct {
	// IDs are exact "Kind/id" references, e.g. "GlobalRule/observability".
	IDs []string `yaml:"ids" json:"ids,omitempty"`
	// Labels protect any resource carrying a matching spec label.
	Labels map[string]string `yaml:"labels" json:"labels,omitempty"`
	// Severity is how a violation is treated. Defaults to deny, because a
	// protected resource that only warns is not protected.
	Severity Severity `yaml:"severity" json:"severity,omitempty"`
}

// Profile is a configurable policy document.
//
// It is deliberately a fixed set of named rules rather than an expression
// language. A rule that can be read at a glance is one a reviewer can reason
// about; an embedded language turns policy review into code review.
type Profile struct {
	APIVersion string `yaml:"apiVersion" json:"api_version"`
	Kind       string `yaml:"kind" json:"kind"`
	Metadata   struct {
		Name string `yaml:"name" json:"name"`
	} `yaml:"metadata" json:"metadata"`
	Spec ProfileSpec `yaml:"spec" json:"spec"`
}

// ProfileSpec is a profile's rules.
type ProfileSpec struct {
	Limits    Limits    `yaml:"limits" json:"limits"`
	Protected Protected `yaml:"protected" json:"protected"`

	// EmbeddedLua controls whether `script` and `filter_func` are permitted.
	// Anyone who can merge a change to one has remote code execution on the
	// data plane, so the default is to deny.
	EmbeddedLua Severity `yaml:"embedded_lua" json:"embedded_lua,omitempty"`

	// PlaintextSecrets controls values shaped like credentials.
	PlaintextSecrets Severity `yaml:"plaintext_secrets" json:"plaintext_secrets,omitempty"`

	// RequireDeleteApproval demands that deletions be approved explicitly at
	// apply time, over and above the tombstone in the repository.
	RequireDeleteApproval bool `yaml:"require_delete_approval" json:"require_delete_approval,omitempty"`

	// RequiredLabels must be present on every managed resource, which is how
	// an organization enforces ownership metadata.
	RequiredLabels []string `yaml:"required_labels" json:"required_labels,omitempty"`
	// RequiredLabelsSeverity defaults to warn.
	RequiredLabelsSeverity Severity `yaml:"required_labels_severity" json:"required_labels_severity,omitempty"`
}

// ProfileAPIVersion is the policy document's format version.
const ProfileAPIVersion = "sixsync/v1alpha1"

// ProfileKind is the policy document's kind.
const ProfileKind = "PolicyProfile"

// DefaultProfile is used when none is configured.
//
// It denies nothing on its own. SixSync's own invariants already prevent the
// dangerous cases, and a tool that blocked changes by default on rules the
// operator never chose would simply be worked around.
func DefaultProfile() *Profile {
	p := &Profile{APIVersion: ProfileAPIVersion, Kind: ProfileKind}
	p.Metadata.Name = "default"
	p.Spec.EmbeddedLua = SeverityWarn
	p.Spec.PlaintextSecrets = SeverityWarn
	p.Spec.RequiredLabelsSeverity = SeverityWarn
	return p
}

// ParseProfile decodes a policy profile strictly.
func ParseProfile(data []byte) (*Profile, error) {
	var p Profile
	if err := yaml.UnmarshalWithOptions(data, &p, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("parsing policy profile: %w", err)
	}
	if p.APIVersion != ProfileAPIVersion {
		return nil, fmt.Errorf("policy profile apiVersion %q is not supported; this build "+
			"implements %q", p.APIVersion, ProfileAPIVersion)
	}
	if p.Kind != ProfileKind {
		return nil, fmt.Errorf("policy profile kind %q is not valid; expected %q", p.Kind, ProfileKind)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate rejects a profile that could not behave as written.
func (p *Profile) Validate() error {
	for name, s := range map[string]Severity{
		"embedded_lua":             p.Spec.EmbeddedLua,
		"plaintext_secrets":        p.Spec.PlaintextSecrets,
		"required_labels_severity": p.Spec.RequiredLabelsSeverity,
		"protected.severity":       p.Spec.Protected.Severity,
	} {
		if err := validSeverity(name, s); err != nil {
			return err
		}
	}

	for name, v := range map[string]*int{
		"max_creates": p.Spec.Limits.MaxCreates, "max_updates": p.Spec.Limits.MaxUpdates,
		"max_deletes": p.Spec.Limits.MaxDeletes, "max_total_changes": p.Spec.Limits.MaxTotalChanges,
		"max_blast_radius": p.Spec.Limits.MaxBlastRadius,
	} {
		if v != nil && *v < 0 {
			return fmt.Errorf("limits.%s must not be negative, got %d", name, *v)
		}
	}

	for _, id := range p.Spec.Protected.IDs {
		if !strings.Contains(id, "/") {
			return fmt.Errorf("protected.ids entry %q must be of the form Kind/id", id)
		}
	}
	return nil
}

func validSeverity(field string, s Severity) error {
	switch s {
	case "", SeverityOff, SeverityWarn, SeverityDeny:
		return nil
	default:
		return fmt.Errorf("%s: %q is not a severity; use off, warn or deny", field, s)
	}
}

// severityOutcome maps a configured severity to an outcome, treating an unset
// severity as the given default.
func severityOutcome(s Severity, fallback Severity) (Outcome, bool) {
	if s == "" {
		s = fallback
	}
	switch s {
	case SeverityDeny:
		return OutcomeDeny, true
	case SeverityWarn:
		return OutcomeWarn, true
	default:
		return OutcomeAllow, false
	}
}

// strongest returns the more severe of two outcomes.
func strongest(a, b Outcome) Outcome {
	rank := map[Outcome]int{OutcomeAllow: 0, OutcomeWarn: 1, OutcomeDeny: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// sortViolations orders violations deterministically for stable output.
func sortViolations(v []Violation) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].Rule != v[j].Rule {
			return v[i].Rule < v[j].Rule
		}
		if v[i].Subject != v[j].Subject {
			return v[i].Subject < v[j].Subject
		}
		return v[i].Message < v[j].Message
	})
}
