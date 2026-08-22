package policy

import (
	"strings"
	"testing"
)

func intp(v int) *int { return &v }

func change(t, kind, id string) Change {
	return Change{Type: t, Kind: kind, ID: id}
}

func violationsFor(r *Result, rule string) []Violation {
	var out []Violation
	for _, v := range r.Violations {
		if v.Rule == rule {
			out = append(out, v)
		}
	}
	return out
}

// --- profile parsing -------------------------------------------------------

func TestParseProfile(t *testing.T) {
	p, err := ParseProfile([]byte(`
apiVersion: sixsync/v1alpha1
kind: PolicyProfile
metadata:
  name: production
spec:
  limits:
    max_deletes: 3
    max_blast_radius: 20
  protected:
    ids:
      - GlobalRule/observability
    labels:
      tier: critical
  embedded_lua: deny
  require_delete_approval: true
  required_labels:
    - owner
`))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if p.Metadata.Name != "production" {
		t.Errorf("name = %q", p.Metadata.Name)
	}
	if p.Spec.Limits.MaxDeletes == nil || *p.Spec.Limits.MaxDeletes != 3 {
		t.Errorf("max_deletes not parsed: %+v", p.Spec.Limits)
	}
	if p.Spec.EmbeddedLua != SeverityDeny {
		t.Errorf("embedded_lua = %q", p.Spec.EmbeddedLua)
	}
	if !p.Spec.RequireDeleteApproval {
		t.Error("require_delete_approval was lost")
	}
}

func TestParseProfileRejectsBadDocuments(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{"wrong apiVersion", "apiVersion: other/v1\nkind: PolicyProfile\n"},
		{"wrong kind", "apiVersion: sixsync/v1alpha1\nkind: NotAProfile\n"},
		{"unknown field", "apiVersion: sixsync/v1alpha1\nkind: PolicyProfile\nnonsense: 1\n"},
		{"bad severity", "apiVersion: sixsync/v1alpha1\nkind: PolicyProfile\nspec:\n  embedded_lua: maybe\n"},
		{"negative limit", "apiVersion: sixsync/v1alpha1\nkind: PolicyProfile\nspec:\n  limits:\n    max_deletes: -1\n"},
		{"protected id without kind", "apiVersion: sixsync/v1alpha1\nkind: PolicyProfile\nspec:\n  protected:\n    ids: [justanid]\n"},
		{"not yaml", "\t\tbroken: ["},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseProfile([]byte(tt.doc)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// A limit of zero means "none permitted" and must be distinguishable from "not
// configured".
func TestZeroLimitIsDistinctFromUnset(t *testing.T) {
	zero := ParseOrFail(t, "spec:\n  limits:\n    max_deletes: 0\n")
	if zero.Spec.Limits.MaxDeletes == nil {
		t.Fatal("a configured limit of 0 was read as unset")
	}

	res := Evaluate(zero, Input{Changes: []Change{change("delete", "Route", "r1")}})
	if !res.Denied() {
		t.Error("max_deletes: 0 should forbid any deletion")
	}

	unset := ParseOrFail(t, "spec:\n  limits: {}\n")
	if unset.Spec.Limits.MaxDeletes != nil {
		t.Error("an unset limit should stay nil")
	}
	if Evaluate(unset, Input{Changes: []Change{change("delete", "Route", "r1")}}).Denied() {
		t.Error("an unconfigured limit must not deny")
	}
}

func ParseOrFail(t *testing.T, specBody string) *Profile {
	t.Helper()
	p, err := ParseProfile([]byte("apiVersion: sixsync/v1alpha1\nkind: PolicyProfile\n" +
		"metadata:\n  name: test\n" + specBody))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	return p
}

// --- default profile -------------------------------------------------------

// A tool that blocked changes on rules the operator never chose would simply be
// worked around.
func TestDefaultProfileDeniesNothing(t *testing.T) {
	in := Input{Changes: []Change{
		change("create", "Route", "r1"),
		change("update", "Service", "s1"),
		change("delete", "Upstream", "u1"),
	}}
	if res := Evaluate(DefaultProfile(), in); res.Denied() {
		t.Errorf("the default profile denied a plain change: %+v", res.Violations)
	}
}

func TestNilProfileUsesTheDefault(t *testing.T) {
	res := Evaluate(nil, Input{Changes: []Change{change("create", "Route", "r1")}})
	if res.Denied() {
		t.Error("a nil profile must behave like the default, not deny")
	}
}

// --- limits ----------------------------------------------------------------

func TestNumericLimits(t *testing.T) {
	tests := []struct {
		name       string
		profile    func(*Profile)
		changes    []Change
		wantDenied bool
		wantRule   string
	}{
		{
			name:    "creations within the limit",
			profile: func(p *Profile) { p.Spec.Limits.MaxCreates = intp(2) },
			changes: []Change{change("create", "Route", "r1")},
		},
		{
			name:       "creations over the limit",
			profile:    func(p *Profile) { p.Spec.Limits.MaxCreates = intp(1) },
			changes:    []Change{change("create", "Route", "r1"), change("create", "Route", "r2")},
			wantDenied: true, wantRule: RuleMaxCreates,
		},
		{
			name:       "deletions over the limit",
			profile:    func(p *Profile) { p.Spec.Limits.MaxDeletes = intp(1) },
			changes:    []Change{change("delete", "Route", "r1"), change("delete", "Route", "r2")},
			wantDenied: true, wantRule: RuleMaxDeletes,
		},
		{
			name:       "total changes over the limit",
			profile:    func(p *Profile) { p.Spec.Limits.MaxTotalChanges = intp(2) },
			changes:    []Change{change("create", "Route", "r1"), change("update", "Route", "r2"), change("delete", "Route", "r3")},
			wantDenied: true, wantRule: RuleMaxTotalChanges,
		},
		{
			name:    "updates counted separately from creates",
			profile: func(p *Profile) { p.Spec.Limits.MaxCreates = intp(1) },
			changes: []Change{change("create", "Route", "r1"), change("update", "Route", "r2")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultProfile()
			tt.profile(p)
			res := Evaluate(p, Input{Changes: tt.changes})

			if res.Denied() != tt.wantDenied {
				t.Errorf("Denied() = %v, want %v: %+v", res.Denied(), tt.wantDenied, res.Violations)
			}
			if tt.wantRule != "" && len(violationsFor(res, tt.wantRule)) == 0 {
				t.Errorf("expected a %s violation, got %+v", tt.wantRule, res.Violations)
			}
		})
	}
}

// The case blast-radius limits exist for: one shared plugin config behind
// hundreds of routes.
func TestBlastRadiusLimit(t *testing.T) {
	p := DefaultProfile()
	p.Spec.Limits.MaxBlastRadius = intp(20)

	c := change("update", "PluginConfig", "auth-standard-v1")
	c.BlastRadius = 306
	res := Evaluate(p, Input{Changes: []Change{c}})

	if !res.Denied() {
		t.Fatalf("a change affecting 306 resources should exceed a limit of 20: %+v", res.Violations)
	}
	v := violationsFor(res, RuleMaxBlastRadius)
	if len(v) != 1 {
		t.Fatalf("expected one violation, got %+v", v)
	}
	if !strings.Contains(v[0].Message, "306") {
		t.Errorf("the message should state the actual reach: %s", v[0].Message)
	}
	// The message should suggest the remedy, not just refuse.
	if !strings.Contains(v[0].Message, "migrat") {
		t.Errorf("the message should suggest a safer path: %s", v[0].Message)
	}
}

// --- protected resources ---------------------------------------------------

func TestProtectedByID(t *testing.T) {
	p := DefaultProfile()
	p.Spec.Protected.IDs = []string{"GlobalRule/observability"}

	res := Evaluate(p, Input{Changes: []Change{change("delete", "GlobalRule", "observability")}})
	if !res.Denied() {
		t.Errorf("a protected resource was not defended: %+v", res.Violations)
	}

	clean := Evaluate(p, Input{Changes: []Change{change("delete", "Route", "ordinary")}})
	if clean.Denied() {
		t.Error("an unprotected resource was blocked")
	}
}

func TestProtectedByLabel(t *testing.T) {
	p := DefaultProfile()
	p.Spec.Protected.Labels = map[string]string{"tier": "critical"}

	c := change("update", "Route", "r1")
	c.Labels = map[string]string{"tier": "critical", "owner": "payments"}

	res := Evaluate(p, Input{Changes: []Change{c}})
	if !res.Denied() {
		t.Errorf("a resource with a protected label was not defended: %+v", res.Violations)
	}
	if !strings.Contains(violationsFor(res, RuleProtectedResources)[0].Message, "tier=critical") {
		t.Error("the message should name the matching label")
	}
}

// A protected resource that only warns is not protected.
func TestProtectedDefaultsToDeny(t *testing.T) {
	p := DefaultProfile()
	p.Spec.Protected.IDs = []string{"Route/r1"}
	// Severity deliberately unset.

	if !Evaluate(p, Input{Changes: []Change{change("update", "Route", "r1")}}).Denied() {
		t.Error("protection should default to deny")
	}
}

func TestProtectedSeverityCanBeRelaxed(t *testing.T) {
	p := DefaultProfile()
	p.Spec.Protected.IDs = []string{"Route/r1"}
	p.Spec.Protected.Severity = SeverityWarn

	res := Evaluate(p, Input{Changes: []Change{change("update", "Route", "r1")}})
	if res.Denied() {
		t.Error("an explicit warn severity should not deny")
	}
	if res.Outcome != OutcomeWarn {
		t.Errorf("Outcome = %s, want warn", res.Outcome)
	}
}

func TestProtectedSeverityOffDisablesTheRule(t *testing.T) {
	p := DefaultProfile()
	p.Spec.Protected.IDs = []string{"Route/r1"}
	p.Spec.Protected.Severity = SeverityOff

	res := Evaluate(p, Input{Changes: []Change{change("update", "Route", "r1")}})
	if len(res.Violations) != 0 {
		t.Errorf("severity off should silence the rule: %+v", res.Violations)
	}
}

// --- embedded Lua ----------------------------------------------------------

func TestEmbeddedLua(t *testing.T) {
	c := change("update", "Route", "r1")
	c.HasEmbeddedLua = true

	warn := Evaluate(DefaultProfile(), Input{Changes: []Change{c}})
	if warn.Denied() {
		t.Error("the default should warn about embedded Lua, not deny")
	}
	if len(violationsFor(warn, RuleNoEmbeddedLua)) != 1 {
		t.Errorf("expected a warning: %+v", warn.Violations)
	}

	strict := DefaultProfile()
	strict.Spec.EmbeddedLua = SeverityDeny
	if !Evaluate(strict, Input{Changes: []Change{c}}).Denied() {
		t.Error("a deny severity should block embedded Lua")
	}
}

func TestEmbeddedLuaMessageExplainsTheRisk(t *testing.T) {
	c := change("update", "Route", "r1")
	c.HasEmbeddedLua = true
	res := Evaluate(DefaultProfile(), Input{Changes: []Change{c}})

	msg := violationsFor(res, RuleNoEmbeddedLua)[0].Message
	if !strings.Contains(msg, "remote code execution") {
		t.Errorf("the message should state the risk plainly: %s", msg)
	}
}

// --- secrets ---------------------------------------------------------------

func TestPlaintextSecrets(t *testing.T) {
	c := change("update", "Route", "r1")
	c.SecretLikeFields = []string{"plugins.key-auth.key"}

	res := Evaluate(DefaultProfile(), Input{Changes: []Change{c}})
	v := violationsFor(res, RuleNoPlaintextSecrets)
	if len(v) != 1 {
		t.Fatalf("expected one violation: %+v", res.Violations)
	}
	if !strings.Contains(v[0].Message, "plugins.key-auth.key") {
		t.Errorf("the message should name the field: %s", v[0].Message)
	}
	if !strings.Contains(v[0].Message, "$secret://") {
		t.Errorf("the message should suggest the remedy: %s", v[0].Message)
	}
	// The violation must not contain the secret itself.
	if strings.Contains(v[0].Message, "actual-secret-value") {
		t.Error("a secret value leaked into a policy violation")
	}
}

// --- delete approval -------------------------------------------------------

func TestDeleteApproval(t *testing.T) {
	p := DefaultProfile()
	p.Spec.RequireDeleteApproval = true
	changes := []Change{change("delete", "Route", "r1")}

	if !Evaluate(p, Input{Changes: changes}).Denied() {
		t.Error("an unapproved deletion should be denied")
	}
	if Evaluate(p, Input{Changes: changes, DeleteApproved: true}).Denied() {
		t.Error("an approved deletion should proceed")
	}
	// No deletions at all: approval is irrelevant.
	if Evaluate(p, Input{Changes: []Change{change("create", "Route", "r1")}}).Denied() {
		t.Error("a plan with no deletions should not need deletion approval")
	}
}

// --- required labels -------------------------------------------------------

func TestRequiredLabels(t *testing.T) {
	p := DefaultProfile()
	p.Spec.RequiredLabels = []string{"owner", "tier"}

	c := change("create", "Route", "r1")
	c.Labels = map[string]string{"owner": "payments"}

	res := Evaluate(p, Input{Changes: []Change{c}})
	v := violationsFor(res, RuleRequireLabels)
	if len(v) != 1 {
		t.Fatalf("expected a violation: %+v", res.Violations)
	}
	if !strings.Contains(v[0].Message, "tier") {
		t.Errorf("the message should name the missing label: %s", v[0].Message)
	}
	if strings.Contains(v[0].Message, "owner") {
		t.Errorf("the message should not name a label that is present: %s", v[0].Message)
	}
}

// A resource being deleted cannot be required to carry labels.
func TestRequiredLabelsSkipDeletions(t *testing.T) {
	p := DefaultProfile()
	p.Spec.RequiredLabels = []string{"owner"}

	res := Evaluate(p, Input{Changes: []Change{change("delete", "Route", "r1")}})
	if len(violationsFor(res, RuleRequireLabels)) != 0 {
		t.Errorf("a deletion should not need labels: %+v", res.Violations)
	}
}

// --- result shape ----------------------------------------------------------

// An operator should see everything wrong in one pass, not fix violations one
// run at a time.
func TestAllRulesAreEvaluatedEvenAfterADenial(t *testing.T) {
	p := DefaultProfile()
	p.Spec.Limits.MaxCreates = intp(0)
	p.Spec.EmbeddedLua = SeverityDeny

	c := change("create", "Route", "r1")
	c.HasEmbeddedLua = true
	c.SecretLikeFields = []string{"plugins.key-auth.key"}

	res := Evaluate(p, Input{Changes: []Change{c}})

	for _, rule := range []string{RuleMaxCreates, RuleNoEmbeddedLua, RuleNoPlaintextSecrets} {
		if len(violationsFor(res, rule)) == 0 {
			t.Errorf("rule %s did not report despite an earlier denial", rule)
		}
	}
}

func TestEvaluatedListsRulesThatRan(t *testing.T) {
	p := DefaultProfile()
	p.Spec.Limits.MaxDeletes = intp(5)

	res := Evaluate(p, Input{Changes: []Change{change("create", "Route", "r1")}})

	var sawDeletes bool
	for _, r := range res.Evaluated {
		if r == RuleMaxDeletes {
			sawDeletes = true
		}
	}
	if !sawDeletes {
		t.Errorf("a configured rule that passed should still be listed as evaluated: %v", res.Evaluated)
	}
	// An unconfigured limit should not appear.
	for _, r := range res.Evaluated {
		if r == RuleMaxCreates {
			t.Errorf("an unconfigured rule was listed as evaluated: %v", res.Evaluated)
		}
	}
}

// Policy output is written into plan artifacts and CI logs.
func TestResultIsDeterministic(t *testing.T) {
	p := DefaultProfile()
	p.Spec.EmbeddedLua = SeverityDeny
	p.Spec.RequiredLabels = []string{"owner", "tier", "env"}

	var changes []Change
	for _, id := range []string{"r5", "r1", "r3", "r2", "r4"} {
		c := change("update", "Route", id)
		c.HasEmbeddedLua = true
		changes = append(changes, c)
	}

	var first string
	for run := 0; run < 40; run++ {
		res := Evaluate(p, Input{Changes: changes})
		var sb strings.Builder
		for _, v := range res.Violations {
			sb.WriteString(v.Rule + "|" + v.Subject + ";")
		}
		got := sb.String()
		if run == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("violation ordering varied:\n%s\n%s", first, got)
		}
	}
}

func TestOutcomeIsTheStrongestViolation(t *testing.T) {
	p := DefaultProfile()
	p.Spec.EmbeddedLua = SeverityWarn
	p.Spec.Limits.MaxCreates = intp(0)

	c := change("create", "Route", "r1")
	c.HasEmbeddedLua = true

	res := Evaluate(p, Input{Changes: []Change{c}})
	if res.Outcome != OutcomeDeny {
		t.Errorf("Outcome = %s; a deny alongside a warn must produce deny", res.Outcome)
	}
}

func TestEmptyChangeSetIsAllowed(t *testing.T) {
	p := DefaultProfile()
	p.Spec.Limits.MaxCreates = intp(0)
	p.Spec.RequireDeleteApproval = true

	res := Evaluate(p, Input{})
	if res.Denied() {
		t.Errorf("an empty plan should never be denied: %+v", res.Violations)
	}
}
