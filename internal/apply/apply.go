// Package apply executes a previously generated plan against APISIX.
//
// This is the only package in SixSync that changes remote state, and every
// design choice in it is about narrowing the window in which something can go
// wrong that the operator did not agree to.
//
// The shape of the guarantee:
//
//   - Apply consumes a plan artifact. It never recomputes one, so the change
//     that was reviewed is the change that runs.
//   - Before the first write, every affected resource is re-read and compared
//     against the revision the plan recorded. Anything that moved aborts the
//     whole run, before anything has changed.
//   - Each write is verified by reading the resource back.
//   - The first unrecoverable failure stops the run. Remaining operations are
//     recorded as skipped rather than attempted.
//
// What this is not: atomic. APISIX has no transaction across resources, so a
// failure partway through leaves some changes applied and some not. SixSync
// records exactly what happened and offers rollback; it does not pretend the
// problem does not exist.
package apply

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/journal"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/plan"
	"github.com/samansohani78/SixSync/internal/redact"
	"github.com/samansohani78/SixSync/internal/resource"
)

// Gateway is the Admin API surface apply needs.
type Gateway interface {
	Get(ctx context.Context, d model.Definition, id string) (apisix.Resource, error)
	List(ctx context.Context, d model.Definition) ([]apisix.Resource, error)
	Put(ctx context.Context, d model.Definition, id string, spec map[string]any) (apisix.Resource, error)
	Delete(ctx context.Context, d model.Definition, id string) error
	BaseURL() string
}

// Options configures an apply run.
type Options struct {
	Plan    *plan.Plan
	Repo    *resource.Repo
	Gateway Gateway

	// JournalPath is where the execution record is written. Required: an apply
	// with no journal cannot be rolled back or explained afterwards.
	JournalPath string

	// Approved records that the operator explicitly approved this run.
	Approved bool
	// DeleteApproved records separate approval for deletions.
	DeleteApproved bool

	// KeyFingerprint identifies the credential in use, so the run can confirm
	// it matches the plan's.
	KeyFingerprint string
	// SixSyncVersion is recorded in the journal.
	SixSyncVersion string

	// Now supplies timestamps; tests replace it.
	Now func() time.Time
}

// Outcome is how an apply ended.
type Outcome string

const (
	// OutcomeSucceeded means every planned operation applied and verified.
	OutcomeSucceeded Outcome = "succeeded"
	// OutcomeNothingToDo means the plan contained no changes.
	OutcomeNothingToDo Outcome = "nothing-to-do"
	// OutcomeStale means remote state moved after the plan was built. Nothing
	// was changed.
	OutcomeStale Outcome = "stale"
	// OutcomePartial means some operations applied and then one failed. Live
	// state is mixed.
	OutcomePartial Outcome = "partial"
	// OutcomeFailed means the run failed before changing anything.
	OutcomeFailed Outcome = "failed"
)

// Report is the result of an apply.
type Report struct {
	Outcome     Outcome
	JournalPath string

	Applied   int
	Failed    int
	Skipped   int
	Uncertain int

	// Conflicts describe resources that moved since the plan was built.
	Conflicts []string
	// Message summarizes the outcome for a human.
	Message string
	// Err is the failure that stopped the run.
	Err error
}

// Changed reports whether any remote state was modified.
func (r *Report) Changed() bool { return r.Applied > 0 || r.Uncertain > 0 }

// Run executes a plan.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if err := validateOptions(opts); err != nil {
		return &Report{Outcome: OutcomeFailed, Err: err}, err
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	p := opts.Plan

	// --- checks that must pass before anything is touched -------------------

	if err := p.Verify(); err != nil {
		return failed(err)
	}
	if blocked := p.Content.Blocked(); len(blocked) > 0 {
		return failed(fmt.Errorf("this plan has %d blocked operation(s) and cannot be applied; "+
			"the first is %s/%s: %s", len(blocked), blocked[0].Kind, blocked[0].ID,
			blocked[0].BlockedReason))
	}
	if p.Content.Denied() {
		return failed(errors.New("policy denied this plan"))
	}
	if err := checkTarget(p, opts); err != nil {
		return failed(err)
	}
	if err := checkApprovals(p, opts); err != nil {
		return failed(err)
	}

	planned := plannedOperations(p)
	if len(planned) == 0 {
		return &Report{
			Outcome: OutcomeNothingToDo,
			Message: "the plan contains no changes; nothing was done",
		}, nil
	}

	// Re-reading every affected resource before the first write is the whole
	// safety story: APISIX offers no conditional write, so this is the only
	// point at which a stale plan can be caught before it does damage.
	conflicts, states, err := checkPreconditions(ctx, opts.Gateway, planned)
	if err != nil {
		return failed(err)
	}
	if len(conflicts) > 0 {
		return &Report{
			Outcome:   OutcomeStale,
			Conflicts: conflicts,
			Message: fmt.Sprintf("%d resource(s) changed on the gateway after this plan was "+
				"built. Nothing was changed. Re-run `sixsync plan` and review the new plan.",
				len(conflicts)),
			Err: errors.New("plan is stale"),
		}, nil
	}

	// --- execution ---------------------------------------------------------

	w, err := journal.Create(opts.JournalPath)
	if err != nil {
		return failed(err)
	}
	defer w.Close() //nolint:errcheck // the footer is already synced

	if err := w.Append(journal.Entry{Type: journal.EntryHeader, Header: &journal.Header{
		FormatVersion:  journal.FormatVersion,
		StartedAt:      now(),
		PlanDigest:     p.Digest,
		AdminURL:       p.Content.Target.AdminURL,
		APISIXVersion:  p.Content.Target.APISIXVersion,
		KeyFingerprint: opts.KeyFingerprint,
		SixSyncVersion: opts.SixSyncVersion,
		OperationCount: len(planned),
	}}); err != nil {
		return failed(err)
	}

	report := &Report{Outcome: OutcomeSucceeded, JournalPath: opts.JournalPath}
	desired := desiredSpecs(opts.Repo)

	var stopped bool
	for _, op := range planned {
		if stopped {
			report.Skipped++
			_ = w.Append(journal.Entry{Type: journal.EntryResult, Result: &journal.Result{
				Order: op.Order, Type: string(op.Type), Kind: op.Kind, ID: op.ID,
				Outcome: journal.OutcomeSkipped, At: now(),
			}})
			continue
		}

		// Record prior state before touching anything, so rollback has
		// something to work from even if the process dies mid-write.
		before := priorState(op, states[opKey(op)])
		if err := w.Append(journal.Entry{Type: journal.EntryBefore, Before: &before}); err != nil {
			return partial(report, err)
		}

		result := executeOne(ctx, opts.Gateway, op, desired, now)
		if err := w.Append(journal.Entry{Type: journal.EntryResult, Result: &result}); err != nil {
			return partial(report, err)
		}

		switch result.Outcome {
		case journal.OutcomeApplied:
			report.Applied++
		case journal.OutcomeUncertain:
			report.Uncertain++
			stopped = true
			report.Err = errors.New(result.Error)
		default:
			report.Failed++
			stopped = true
			report.Err = errors.New(result.Error)
		}
	}

	switch {
	case report.Failed == 0 && report.Uncertain == 0:
		report.Message = fmt.Sprintf("%d operation(s) applied and verified", report.Applied)
	case report.Applied == 0 && report.Uncertain == 0:
		report.Outcome = OutcomeFailed
		report.Message = "the first operation failed; no change was applied"
	default:
		report.Outcome = OutcomePartial
		report.Message = fmt.Sprintf("%d operation(s) applied, then one did not. Live state is "+
			"mixed. Run `sixsync rollback --journal %s` to restore, or inspect the journal.",
			report.Applied, opts.JournalPath)
	}
	if report.Uncertain > 0 {
		report.Message += ". One operation's outcome is UNKNOWN: the request was sent but no " +
			"response arrived, so it may or may not have been applied. Verify it by hand before " +
			"doing anything else."
	}

	_ = w.Append(journal.Entry{Type: journal.EntryFooter, Footer: &journal.Footer{
		FinishedAt: now(),
		Outcome:    journalOutcome(report),
		Applied:    report.Applied, Failed: report.Failed,
		Skipped: report.Skipped, Uncertain: report.Uncertain,
		Message: report.Message,
	}})

	return report, report.Err
}

func validateOptions(o Options) error {
	switch {
	case o.Plan == nil:
		return errors.New("no plan was provided")
	case o.Gateway == nil:
		return errors.New("no Admin API client was provided")
	case o.Repo == nil:
		return errors.New("no repository was loaded")
	case o.JournalPath == "":
		return errors.New("a journal path is required; an apply that cannot be recorded " +
			"cannot be rolled back or explained afterwards")
	}
	return nil
}

func failed(err error) (*Report, error) {
	return &Report{Outcome: OutcomeFailed, Err: err, Message: err.Error()}, err
}

func partial(r *Report, err error) (*Report, error) {
	r.Outcome = OutcomePartial
	r.Err = err
	r.Message = err.Error()
	return r, err
}

func journalOutcome(r *Report) journal.Outcome {
	switch {
	case r.Uncertain > 0:
		return journal.OutcomeUncertain
	case r.Failed > 0:
		return journal.OutcomeFailed
	default:
		return journal.OutcomeApplied
	}
}

// checkTarget refuses a plan built for a different gateway or credential.
//
// Applying a production plan to staging, or the reverse, is a mistake a busy
// operator makes with one wrong environment variable.
func checkTarget(p *plan.Plan, o Options) error {
	if got, want := o.Gateway.BaseURL(), p.Content.Target.AdminURL; got != want {
		return fmt.Errorf("this plan was built for %s but the configured endpoint is %s; "+
			"refusing to apply a plan to a different gateway", want, got)
	}
	if p.Content.Target.KeyFingerprint != "" && o.KeyFingerprint != "" &&
		p.Content.Target.KeyFingerprint != o.KeyFingerprint {
		return fmt.Errorf("this plan was built with a different Admin API key (%s) than the one "+
			"configured now (%s); re-run `sixsync plan` with the credential you intend to apply "+
			"with", p.Content.Target.KeyFingerprint, o.KeyFingerprint)
	}
	return nil
}

// checkApprovals enforces that a mutation was explicitly asked for.
func checkApprovals(p *plan.Plan, o Options) error {
	if !o.Approved {
		return errors.New("apply requires explicit approval; re-run with --approve once you " +
			"have reviewed the plan")
	}
	if !o.DeleteApproved {
		for _, op := range p.Content.Operations {
			if op.Status == plan.StatusPlanned && op.Type == plan.OpDelete {
				return fmt.Errorf("this plan deletes %s/%s and deletion needs its own approval; "+
					"re-run with --approve-delete", op.Kind, op.ID)
			}
		}
	}
	return nil
}

func plannedOperations(p *plan.Plan) []plan.Operation {
	var out []plan.Operation
	for _, op := range p.Content.Operations {
		if op.Status == plan.StatusPlanned {
			out = append(out, op)
		}
	}
	return out
}

func opKey(op plan.Operation) string { return string(op.Kind) + "/" + op.ID }

// observedState is a resource as it stands immediately before apply.
type observedState struct {
	exists        bool
	value         map[string]any
	digest        string
	modifiedIndex int64
}

// checkPreconditions re-reads every affected resource and compares it against
// what the plan recorded.
//
// This runs to completion before any write, so a stale plan is refused with the
// gateway untouched — and it reports every conflict rather than the first, so an
// operator sees the whole picture in one go.
func checkPreconditions(
	ctx context.Context, g Gateway, ops []plan.Operation,
) ([]string, map[string]observedState, error) {
	states := make(map[string]observedState, len(ops))
	var conflicts []string

	for _, op := range ops {
		def, ok := model.Lookup(op.Kind)
		if !ok {
			return nil, nil, fmt.Errorf("unknown kind %q in plan", op.Kind)
		}

		current, err := g.Get(ctx, def, op.ID)
		switch {
		case err == nil:
			canon, cErr := canonical.Canonicalize(op.Kind, current.Value)
			if cErr != nil {
				return nil, nil, fmt.Errorf("%s/%s: %w", op.Kind, op.ID, cErr)
			}
			digest, dErr := canonical.Digest(canon)
			if dErr != nil {
				return nil, nil, dErr
			}
			states[opKey(op)] = observedState{
				exists: true, value: current.Value,
				digest: digest, modifiedIndex: current.ModifiedIndex,
			}

		case apisix.IsKind(err, apisix.ErrNotFound):
			states[opKey(op)] = observedState{exists: false}

		default:
			return nil, nil, fmt.Errorf("re-reading %s/%s before apply: %w", op.Kind, op.ID, err)
		}

		if c := conflictFor(op, states[opKey(op)]); c != "" {
			conflicts = append(conflicts, c)
		}
	}
	return conflicts, states, nil
}

// conflictFor reports whether a resource no longer matches its precondition.
func conflictFor(op plan.Operation, now observedState) string {
	pre := op.Precondition

	if pre.MustNotExist {
		if now.exists {
			return fmt.Sprintf("%s/%s was created since the plan was built", op.Kind, op.ID)
		}
		return ""
	}

	if !now.exists {
		return fmt.Sprintf("%s/%s was deleted since the plan was built", op.Kind, op.ID)
	}

	// modifiedIndex advances on every write, including one storing identical
	// content, so it detects a concurrent writer even when nothing changed —
	// which is the conservative reading, and the one worth having.
	if pre.ObservedModifiedIndex != 0 && now.modifiedIndex != pre.ObservedModifiedIndex {
		return fmt.Sprintf("%s/%s was modified since the plan was built (revision %d, plan saw %d)",
			op.Kind, op.ID, now.modifiedIndex, pre.ObservedModifiedIndex)
	}
	// The digest is a second check for the case where a revision is
	// unavailable or was reused.
	if pre.ObservedDigest != "" && now.digest != pre.ObservedDigest {
		return fmt.Sprintf("%s/%s content changed since the plan was built", op.Kind, op.ID)
	}
	return ""
}

// priorState builds the journal record of what a resource looked like before.
func priorState(op plan.Operation, before observedState) journal.Before {
	b := journal.Before{
		Kind: op.Kind, ID: op.ID,
		Existed:       before.exists,
		Digest:        before.digest,
		ModifiedIndex: before.modifiedIndex,
	}
	if !before.exists {
		return b
	}

	// Strip what APISIX will not accept back. It sets create_time, update_time
	// and the identity field itself, and rejects a write that carries any of
	// them with `the property is forbidden`. Storing them would make every
	// rollback of an updated resource fail — which is exactly what happened
	// before this was fixed.
	writable := make(map[string]any, len(before.value))
	for k, v := range before.value {
		writable[k] = v
	}
	if def, ok := model.Lookup(op.Kind); ok {
		delete(writable, def.IdentityField)
	}
	delete(writable, "create_time")
	delete(writable, "update_time")

	// The journal is written to disk and often kept as a CI artifact, so the
	// recorded state is redacted. Rollback consequently cannot restore a secret
	// it was never given, and says so instead of writing a placeholder into the
	// gateway.
	policy := model.RedactionPolicy(op.Kind)
	redacted, ok := policy.Apply(writable).(map[string]any)
	if !ok {
		redacted = map[string]any{}
	}
	b.State = redacted
	b.RedactedFields = redactedPaths(writable, redacted, nil)
	return b
}

// redactedPaths lists the fields whose values were replaced.
func redactedPaths(original, redacted map[string]any, prefix []string) []string {
	var out []string
	for k, rv := range redacted {
		path := append(append([]string{}, prefix...), k)
		if s, ok := rv.(string); ok && s == redact.Placeholder {
			if ov, present := original[k]; !present || ov != redact.Placeholder {
				out = append(out, joinDots(path))
			}
			continue
		}
		rm, rIsMap := rv.(map[string]any)
		om, oIsMap := original[k].(map[string]any)
		if rIsMap && oIsMap {
			out = append(out, redactedPaths(om, rm, path)...)
		}
	}
	return out
}

func joinDots(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += "."
		}
		s += p
	}
	return s
}

// desiredSpecs indexes the repository's specs by resource key.
func desiredSpecs(repo *resource.Repo) map[string]map[string]any {
	out := make(map[string]map[string]any, len(repo.Resources))
	for _, l := range repo.Resources {
		out[string(l.File.Kind)+"/"+l.File.Metadata.ID] = l.File.Spec
	}
	return out
}
