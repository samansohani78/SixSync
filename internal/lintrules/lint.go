// Package lintrules checks a SixSync repository without contacting APISIX.
//
// Every rule has a stable identifier so that a finding can be suppressed,
// searched for, or referred to in a review comment. Rules are deliberately few
// and each one is justified: a linter that emits findings an operator learns to
// ignore is worse than no linter, because it trains people to skim.
package lintrules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/redact"
	"github.com/samansohani78/SixSync/internal/resource"
)

// Severity is how seriously to take a finding.
type Severity string

const (
	// SeverityError means the repository is wrong: applying it would fail or
	// do something other than what it says.
	SeverityError Severity = "error"

	// SeverityWarning means the repository is valid but something deserves a
	// human decision.
	SeverityWarning Severity = "warning"
)

// Rule identifiers. These are a public interface: they appear in output and in
// review comments, so they must stay stable.
const (
	RuleDuplicateID    = "duplicate-id"
	RuleInvalidID      = "invalid-id"
	RuleDanglingRef    = "dangling-reference"
	RuleLuaCode        = "embedded-lua"
	RulePlaintextKey   = "plaintext-secret"
	RuleEphemeralRoute = "ephemeral-route"
	RuleAbsentNoReason = "deletion-without-reason"
)

// Finding is one problem with one file.
type Finding struct {
	Rule     string     `json:"rule"`
	Severity Severity   `json:"severity"`
	Path     string     `json:"path"`
	Kind     model.Kind `json:"kind"`
	ID       string     `json:"id"`
	Message  string     `json:"message"`
}

// Check runs every rule over a loaded repository and returns findings ordered
// deterministically.
func Check(repo *resource.Repo) []Finding {
	var out []Finding

	out = append(out, checkDuplicateIDs(repo)...)
	out = append(out, checkReferences(repo)...)
	out = append(out, checkRouteCollisions(repo)...)
	out = append(out, checkFilenameCollisions(repo)...)
	out = append(out, checkCaseOnlyIDs(repo)...)
	out = append(out, checkUncomparable(repo)...)

	for _, l := range repo.Resources {
		out = append(out, checkOneFile(l)...)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// checkDuplicateIDs finds two files claiming the same resource.
//
// This matters because the last write would win at apply time, silently, and
// which file that is depends on directory ordering.
func checkDuplicateIDs(repo *resource.Repo) []Finding {
	byKey := map[resource.Key][]resource.Loaded{}
	for _, l := range repo.Resources {
		k := resource.Key{Kind: l.File.Kind, ID: l.File.Metadata.ID}
		byKey[k] = append(byKey[k], l)
	}

	var out []Finding
	for key, group := range byKey {
		if len(group) < 2 {
			continue
		}
		for _, l := range group {
			var others []string
			for _, o := range group {
				if o.Path != l.Path {
					others = append(others, o.Path)
				}
			}
			sort.Strings(others)
			out = append(out, Finding{
				Rule: RuleDuplicateID, Severity: SeverityError,
				Path: l.Path, Kind: key.Kind, ID: key.ID,
				Message: fmt.Sprintf("%s is also declared in %s; at apply time one would "+
					"silently overwrite the other", key, strings.Join(others, ", ")),
			})
		}
	}
	return out
}

// checkReferences finds references to resources no file declares.
//
// APISIX enforces this for some kinds but not others: deleting a plugin config
// that a route still references is allowed, and only fails later as a 503 on
// live traffic. Catching it during review is the point.
func checkReferences(repo *resource.Repo) []Finding {
	declared := map[resource.Key]bool{}
	for _, l := range repo.Resources {
		if l.File.Lifecycle.State == resource.StatePresent {
			declared[resource.Key{Kind: l.File.Kind, ID: l.File.Metadata.ID}] = true
		}
	}

	var out []Finding
	for _, l := range repo.Resources {
		if l.File.Lifecycle.State != resource.StatePresent {
			continue
		}
		def, ok := model.Lookup(l.File.Kind)
		if !ok {
			continue
		}

		for _, ref := range def.References {
			raw, present := l.File.Spec[ref.Field]
			if !present || raw == nil {
				continue
			}
			target := fmt.Sprintf("%v", raw)

			if declared[resource.Key{Kind: ref.Target, ID: target}] {
				continue
			}
			// A reference to a resource this repository does not manage is
			// normal during incremental adoption, so this is a warning rather
			// than an error: the target may well exist in APISIX.
			out = append(out, Finding{
				Rule: RuleDanglingRef, Severity: SeverityWarning,
				Path: l.Path, Kind: l.File.Kind, ID: l.File.Metadata.ID,
				Message: fmt.Sprintf("%s refers to %s/%s, which no file in this repository "+
					"declares; confirm it exists in APISIX", ref.Field, ref.Target, target),
			})
		}
	}
	return out
}

func checkOneFile(l resource.Loaded) []Finding {
	var out []Finding
	f := l.File

	add := func(rule string, sev Severity, msg string) {
		out = append(out, Finding{
			Rule: rule, Severity: sev, Path: l.Path,
			Kind: f.Kind, ID: f.Metadata.ID, Message: msg,
		})
	}

	if err := resource.ValidateID(f.Metadata.ID); err != nil {
		add(RuleInvalidID, SeverityError, err.Error())
	}

	// A deletion should carry its justification, so the reviewer of the diff
	// can see why without hunting for the commit message.
	if f.Lifecycle.State == resource.StateAbsent && f.Lifecycle.Reason == "" {
		add(RuleAbsentNoReason, SeverityWarning,
			"marked for deletion without lifecycle.reason; record why so the diff explains itself")
	}

	// `script` and `filter_func` are executed as Lua by the gateway. Anyone who
	// can merge a change to one of these has remote code execution on the data
	// plane, which is a different level of trust from editing a route match.
	for _, field := range []string{"script", "filter_func"} {
		if v, present := f.Spec[field]; present && v != nil && v != "" {
			add(RuleLuaCode, SeverityWarning, fmt.Sprintf(
				"spec.%s is executed as Lua on the gateway; a change here is remote code "+
					"execution and should be reviewed accordingly", field))
		}
	}

	// A route created with a TTL expires by itself, so the repository would
	// describe a resource that keeps vanishing and reappearing as drift.
	if f.Kind == model.KindRoute {
		if _, present := f.Spec["ttl"]; present {
			add(RuleEphemeralRoute, SeverityWarning,
				"spec.ttl makes this route expire on its own, which will appear as recurring drift")
		}
	}

	out = append(out, checkPlaintextSecrets(l)...)
	return out
}

// checkPlaintextSecrets warns about values shaped like credentials.
//
// This is a heuristic backstop for human error, not a security control. It has
// both false positives and false negatives, so it warns rather than erroring —
// but a real credential committed to Git is worth some noise to catch.
func checkPlaintextSecrets(l resource.Loaded) []Finding {
	var out []Finding
	walkStrings(l.File.Spec, nil, func(path []string, s string) {
		if !redact.LooksSecret(s) {
			return
		}
		out = append(out, Finding{
			Rule: RulePlaintextKey, Severity: SeverityWarning,
			Path: l.Path, Kind: l.File.Kind, ID: l.File.Metadata.ID,
			Message: fmt.Sprintf("spec.%s looks like credential material; if it is, move it out "+
				"of Git and use an APISIX secret reference such as $env:// or $secret://",
				strings.Join(path, ".")),
		})
	})
	return out
}

// walkStrings visits every string value, tracking its path. Map keys are
// visited in sorted order so findings come out in a stable sequence.
func walkStrings(v any, path []string, fn func([]string, string)) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := make([]string, len(path)+1)
			copy(child, path)
			child[len(path)] = k
			walkStrings(t[k], child, fn)
		}
	case []any:
		for _, item := range t {
			walkStrings(item, path, fn)
		}
	case string:
		fn(path, t)
	}
}

// HasErrors reports whether any finding is severe enough to fail a build.
func HasErrors(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}
