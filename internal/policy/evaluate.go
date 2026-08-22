package policy

import (
	"fmt"
	"sort"
	"strings"
)

// Change is one planned change, described in the terms policy reasons about.
//
// This is deliberately a narrow view rather than the plan type itself: policy
// must not depend on the plan package, or the two could not be tested
// independently and a plan change would ripple into policy rules.
type Change struct {
	// Type is "create", "update" or "delete".
	Type string
	// Kind and ID identify the resource.
	Kind string
	ID   string
	// Labels are the resource's spec labels, used by protection rules.
	Labels map[string]string
	// BlastRadius is how many other resources this change affects.
	BlastRadius int
	// HasEmbeddedLua reports a `script` or `filter_func` field.
	HasEmbeddedLua bool
	// SecretLikeFields are spec paths holding values shaped like credentials.
	SecretLikeFields []string
}

// Subject renders a change as "Kind/id".
func (c Change) Subject() string { return c.Kind + "/" + c.ID }

// Input is everything an evaluation needs.
type Input struct {
	Changes []Change
	// DeleteApproved reports whether the operator supplied explicit deletion
	// approval, which RequireDeleteApproval checks.
	DeleteApproved bool
}

// Evaluate applies a profile to a planned change.
//
// Every configured rule is evaluated even after one denies, so an operator sees
// everything wrong in one pass rather than fixing violations one run at a time.
func Evaluate(p *Profile, in Input) *Result {
	if p == nil {
		p = DefaultProfile()
	}

	res := &Result{ProfileName: p.Metadata.Name, Outcome: OutcomeAllow}
	add := func(rule string, outcome Outcome, subject, msg string) {
		if outcome == OutcomeAllow {
			return
		}
		res.Violations = append(res.Violations, Violation{
			Rule: rule, Outcome: outcome, Subject: subject, Message: msg,
		})
		res.Outcome = strongest(res.Outcome, outcome)
	}

	var creates, updates, deletes int
	for _, c := range in.Changes {
		switch c.Type {
		case "create":
			creates++
		case "update":
			updates++
		case "delete":
			deletes++
		}
	}
	total := creates + updates + deletes

	// --- numeric limits ----------------------------------------------------
	// A limit is always deny when configured: an operator who writes
	// "at most 5 deletions" means it as a bound, not a suggestion.
	limits := []struct {
		rule  string
		limit *int
		got   int
		what  string
	}{
		{RuleMaxCreates, p.Spec.Limits.MaxCreates, creates, "creations"},
		{RuleMaxUpdates, p.Spec.Limits.MaxUpdates, updates, "updates"},
		{RuleMaxDeletes, p.Spec.Limits.MaxDeletes, deletes, "deletions"},
		{RuleMaxTotalChanges, p.Spec.Limits.MaxTotalChanges, total, "changes in total"},
	}
	for _, l := range limits {
		if l.limit == nil {
			continue
		}
		res.Evaluated = append(res.Evaluated, l.rule)
		if l.got > *l.limit {
			add(l.rule, OutcomeDeny, "",
				fmt.Sprintf("this plan makes %d %s; the profile permits at most %d",
					l.got, l.what, *l.limit))
		}
	}

	// --- blast radius ------------------------------------------------------
	if p.Spec.Limits.MaxBlastRadius != nil {
		res.Evaluated = append(res.Evaluated, RuleMaxBlastRadius)
		for _, c := range in.Changes {
			if c.BlastRadius > *p.Spec.Limits.MaxBlastRadius {
				add(RuleMaxBlastRadius, OutcomeDeny, c.Subject(),
					fmt.Sprintf("changing this affects %d other resources; the profile permits "+
						"at most %d. Consider creating a new version and migrating in batches",
						c.BlastRadius, *p.Spec.Limits.MaxBlastRadius))
			}
		}
	}

	// --- protected resources ----------------------------------------------
	if len(p.Spec.Protected.IDs) > 0 || len(p.Spec.Protected.Labels) > 0 {
		res.Evaluated = append(res.Evaluated, RuleProtectedResources)
		// Default to deny: a protected resource that only warns is not protected.
		outcome, active := severityOutcome(p.Spec.Protected.Severity, SeverityDeny)

		protectedIDs := map[string]struct{}{}
		for _, id := range p.Spec.Protected.IDs {
			protectedIDs[id] = struct{}{}
		}

		if active {
			for _, c := range in.Changes {
				reason := ""
				if _, ok := protectedIDs[c.Subject()]; ok {
					reason = "it is listed in protected.ids"
				} else if label, value := matchLabel(c.Labels, p.Spec.Protected.Labels); label != "" {
					reason = fmt.Sprintf("it carries the protected label %s=%s", label, value)
				}
				if reason == "" {
					continue
				}
				add(RuleProtectedResources, outcome, c.Subject(),
					fmt.Sprintf("this resource is protected because %s, and the plan would %s it",
						reason, c.Type))
			}
		}
	}

	// --- embedded Lua ------------------------------------------------------
	res.Evaluated = append(res.Evaluated, RuleNoEmbeddedLua)
	if outcome, active := severityOutcome(p.Spec.EmbeddedLua, SeverityWarn); active {
		for _, c := range in.Changes {
			if !c.HasEmbeddedLua {
				continue
			}
			add(RuleNoEmbeddedLua, outcome, c.Subject(),
				"this resource carries a script or filter_func field, which APISIX executes as "+
					"Lua on the data plane; changing it is remote code execution")
		}
	}

	// --- plaintext secrets -------------------------------------------------
	res.Evaluated = append(res.Evaluated, RuleNoPlaintextSecrets)
	if outcome, active := severityOutcome(p.Spec.PlaintextSecrets, SeverityWarn); active {
		for _, c := range in.Changes {
			if len(c.SecretLikeFields) == 0 {
				continue
			}
			fields := append([]string(nil), c.SecretLikeFields...)
			sort.Strings(fields)
			add(RuleNoPlaintextSecrets, outcome, c.Subject(),
				fmt.Sprintf("these fields hold values shaped like credentials: %s. Move them out "+
					"of Git and use an APISIX secret reference such as $env:// or $secret://",
					strings.Join(fields, ", ")))
		}
	}

	// --- deletion approval -------------------------------------------------
	if p.Spec.RequireDeleteApproval {
		res.Evaluated = append(res.Evaluated, RuleDeleteNeedsApproval)
		if deletes > 0 && !in.DeleteApproved {
			add(RuleDeleteNeedsApproval, OutcomeDeny, "",
				fmt.Sprintf("this plan deletes %d resource(s) and the profile requires explicit "+
					"approval; re-run with the deletion approval flag", deletes))
		}
	}

	// --- required labels ---------------------------------------------------
	if len(p.Spec.RequiredLabels) > 0 {
		res.Evaluated = append(res.Evaluated, RuleRequireLabels)
		if outcome, active := severityOutcome(p.Spec.RequiredLabelsSeverity, SeverityWarn); active {
			for _, c := range in.Changes {
				// A deletion cannot be required to carry labels.
				if c.Type == "delete" {
					continue
				}
				var missing []string
				for _, want := range p.Spec.RequiredLabels {
					if _, ok := c.Labels[want]; !ok {
						missing = append(missing, want)
					}
				}
				if len(missing) == 0 {
					continue
				}
				sort.Strings(missing)
				add(RuleRequireLabels, outcome, c.Subject(),
					fmt.Sprintf("missing required label(s): %s", strings.Join(missing, ", ")))
			}
		}
	}

	sortViolations(res.Violations)
	sort.Strings(res.Evaluated)
	res.Evaluated = dedupe(res.Evaluated)
	return res
}

// matchLabel returns the first protected label a resource carries.
//
// Keys are checked in sorted order so that a resource matching several
// protected labels always reports the same one.
func matchLabel(have, protected map[string]string) (string, string) {
	if len(have) == 0 || len(protected) == 0 {
		return "", ""
	}
	keys := make([]string, 0, len(protected))
	for k := range protected {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if v, ok := have[k]; ok && v == protected[k] {
			return k, v
		}
	}
	return "", ""
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
