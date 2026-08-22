package lintrules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/resource"
)

// Collision rule identifiers.
const (
	RuleRouteCollision    = "route-collision"
	RuleFilenameCollision = "filename-collision"
	RuleCaseOnlyID        = "case-only-id-difference"
)

// checkRouteCollisions finds routes that match the same requests.
//
// Two routes with identical match criteria are a silent problem: APISIX picks
// one by priority and then by an internal ordering, so which one serves a
// request is not something the repository states. Traffic goes somewhere the
// configuration does not say, and the symptom appears later as "that plugin
// isn't applying".
func checkRouteCollisions(repo *resource.Repo) []Finding {
	type routeKey struct {
		signature string
		priority  string
	}

	byMatch := map[routeKey][]resource.Loaded{}

	for _, l := range repo.Resources {
		if l.File.Kind != model.KindRoute || l.File.Lifecycle.State != resource.StatePresent {
			continue
		}
		sig, ok := routeSignature(l.File.Spec)
		if !ok {
			continue
		}
		// Priority is APISIX's own tie-breaker, so routes at different
		// priorities are ordered deliberately and do not collide.
		key := routeKey{signature: sig, priority: scalarString(l.File.Spec["priority"])}
		byMatch[key] = append(byMatch[key], l)
	}

	var out []Finding
	for key, group := range byMatch {
		if len(group) < 2 {
			continue
		}
		for _, l := range group {
			var others []string
			for _, o := range group {
				if o.Path != l.Path {
					others = append(others, o.File.Metadata.ID)
				}
			}
			sort.Strings(others)
			out = append(out, Finding{
				Rule: RuleRouteCollision, Severity: SeverityWarning,
				Path: l.Path, Kind: model.KindRoute, ID: l.File.Metadata.ID,
				Message: fmt.Sprintf("matches the same requests as %s (%s). APISIX picks one by "+
					"an ordering this repository does not state; give them different priorities "+
					"or make the match criteria distinct",
					strings.Join(others, ", "), key.signature),
			})
		}
	}
	return out
}

// routeSignature renders a route's match criteria in a canonical form.
//
// It reports false when the route matches on something this rule cannot
// compare — `vars`, `filter_func`, or a `remote_addr` restriction — because
// two routes that agree on uri and methods may still be disjoint on those, and
// warning about a collision that does not exist is worse than staying quiet.
func routeSignature(spec map[string]any) (string, bool) {
	for _, narrowing := range []string{"vars", "filter_func", "remote_addr", "remote_addrs"} {
		if v, present := spec[narrowing]; present && v != nil {
			return "", false
		}
	}

	uris := stringSet(spec, "uri", "uris")
	if len(uris) == 0 {
		return "", false
	}
	hosts := stringSet(spec, "host", "hosts")
	methods := stringSet(spec, "methods")

	var b strings.Builder
	b.WriteString("uri=" + strings.Join(uris, "|"))
	if len(hosts) > 0 {
		b.WriteString(" host=" + strings.Join(hosts, "|"))
	}
	if len(methods) > 0 {
		b.WriteString(" methods=" + strings.Join(methods, "|"))
	} else {
		// An absent method list matches every method, which is a wider net
		// than any explicit list and must not compare equal to one.
		b.WriteString(" methods=<any>")
	}
	return b.String(), true
}

// stringSet collects a field that may be singular or plural, sorted.
func stringSet(spec map[string]any, fields ...string) []string {
	var out []string
	for _, f := range fields {
		switch v := spec[f].(type) {
		case string:
			out = append(out, v)
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func scalarString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// checkFilenameCollisions finds resources whose generated file names collide.
//
// `import` derives a file name from the resource, so a collision means one
// resource would overwrite another's file on the next import — losing whichever
// was written first, silently.
func checkFilenameCollisions(repo *resource.Repo) []Finding {
	byName := map[string][]resource.Loaded{}
	for _, l := range repo.Resources {
		dir, err := resource.DirName(l.File.Kind)
		if err != nil {
			continue
		}
		byName[dir+"/"+resource.FileName(l.File)] = append(byName[dir+"/"+resource.FileName(l.File)], l)
	}

	var out []Finding
	for name, group := range byName {
		if len(group) < 2 {
			continue
		}
		for _, l := range group {
			var others []string
			for _, o := range group {
				if o.File.Metadata.ID != l.File.Metadata.ID {
					others = append(others, o.File.Metadata.ID)
				}
			}
			sort.Strings(others)
			out = append(out, Finding{
				Rule: RuleFilenameCollision, Severity: SeverityError,
				Path: l.Path, Kind: l.File.Kind, ID: l.File.Metadata.ID,
				Message: fmt.Sprintf("would be imported to %s, the same file as %s; one would "+
					"silently overwrite the other", name, strings.Join(others, ", ")),
			})
		}
	}
	return out
}

// checkCaseOnlyIDs finds identifiers differing only in case.
//
// APISIX treats them as distinct resources, but a case-insensitive filesystem —
// macOS and Windows by default — maps their files onto one path. A repository
// that works on Linux then loses a resource when someone clones it on a Mac.
func checkCaseOnlyIDs(repo *resource.Repo) []Finding {
	type ident struct {
		kind model.Kind
		id   string
	}
	byLower := map[ident][]resource.Loaded{}

	for _, l := range repo.Resources {
		key := ident{kind: l.File.Kind, id: strings.ToLower(l.File.Metadata.ID)}
		byLower[key] = append(byLower[key], l)
	}

	var out []Finding
	for _, group := range byLower {
		if len(group) < 2 {
			continue
		}
		// Identical identifiers are the duplicate-id rule's business.
		distinct := map[string]struct{}{}
		for _, l := range group {
			distinct[l.File.Metadata.ID] = struct{}{}
		}
		if len(distinct) < 2 {
			continue
		}

		for _, l := range group {
			var others []string
			for id := range distinct {
				if id != l.File.Metadata.ID {
					others = append(others, id)
				}
			}
			sort.Strings(others)
			out = append(out, Finding{
				Rule: RuleCaseOnlyID, Severity: SeverityWarning,
				Path: l.Path, Kind: l.File.Kind, ID: l.File.Metadata.ID,
				Message: fmt.Sprintf("differs only in case from %s. APISIX treats these as "+
					"separate resources, but a case-insensitive filesystem maps their files "+
					"onto one path, so a clone on macOS or Windows would lose one",
					strings.Join(others, ", ")),
			})
		}
	}
	return out
}

// checkUncomparable finds resources the canonicalizer cannot compare.
//
// A resource SixSync cannot canonicalize cannot be diffed either, so drift and
// plan would be quietly wrong about it. Better to say so at lint time.
func checkUncomparable(repo *resource.Repo) []Finding {
	var out []Finding
	for _, l := range repo.Resources {
		if l.File.Lifecycle.State != resource.StatePresent {
			continue
		}
		if _, err := canonical.Canonicalize(l.File.Kind, l.File.Spec); err != nil {
			out = append(out, Finding{
				Rule: RuleUncomparable, Severity: SeverityError,
				Path: l.Path, Kind: l.File.Kind, ID: l.File.Metadata.ID,
				Message: fmt.Sprintf("cannot be compared with live state: %v. drift and plan "+
					"would be unreliable for this resource", err),
			})
		}
	}
	return out
}

// RuleUncomparable marks a resource the canonicalizer refuses.
const RuleUncomparable = "uncomparable-resource"
