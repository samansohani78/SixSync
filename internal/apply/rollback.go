package apply

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/journal"
	"github.com/samansohani78/SixSync/internal/model"
)

// RollbackOptions configures a rollback.
type RollbackOptions struct {
	Journal *journal.Journal
	Gateway Gateway

	// Force proceeds even when a resource has changed since the apply. Off by
	// default: overwriting someone else's later change while "restoring" is a
	// second incident on top of the first.
	Force bool

	// KeyFingerprint identifies the credential in use.
	KeyFingerprint string

	// JournalPath is where the rollback's own record is written.
	JournalPath string

	Now func() time.Time
}

// RollbackOutcome is how a rollback ended.
type RollbackOutcome string

const (
	// RollbackSucceeded means every resource was restored.
	RollbackSucceeded RollbackOutcome = "succeeded"
	// RollbackNothingToDo means the apply changed nothing.
	RollbackNothingToDo RollbackOutcome = "nothing-to-do"
	// RollbackPartial means some resources were restored and some were not.
	RollbackPartial RollbackOutcome = "partial"
	// RollbackBlocked means the rollback did not start, because state had moved.
	RollbackBlocked RollbackOutcome = "blocked"
	// RollbackManualRequired means at least one resource cannot be restored by
	// SixSync at all, and a person has to do it.
	RollbackManualRequired RollbackOutcome = "manual-recovery-required"
)

// RollbackItem is one resource's restoration outcome.
type RollbackItem struct {
	Kind    model.Kind
	ID      string
	Action  string
	Done    bool
	Message string
}

// RollbackReport is the result of a rollback.
type RollbackReport struct {
	Outcome  RollbackOutcome
	Items    []RollbackItem
	Restored int
	Failed   int

	// ManualSteps are instructions for what SixSync could not do itself.
	ManualSteps []string
	Message     string
	Err         error
}

// Rollback restores the resources an apply changed, using its journal.
//
// This is best effort, and the limits are real:
//
//   - It is not a transaction. Resources are restored one at a time, and a
//     failure partway leaves a partial restoration.
//   - It cannot restore a secret it never had. Journals are redacted, so any
//     credential in the prior state is reported as a manual step rather than
//     written back as a placeholder.
//   - It cannot undo effects. Traffic that was routed during the window was
//     routed.
//
// Anything it cannot do is reported as an explicit manual step rather than
// quietly skipped.
func Rollback(ctx context.Context, opts RollbackOptions) (*RollbackReport, error) {
	if opts.Journal == nil {
		return nil, errors.New("no journal was provided")
	}
	if opts.Gateway == nil {
		return nil, errors.New("no Admin API client was provided")
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	j := opts.Journal

	if got, want := opts.Gateway.BaseURL(), j.Header.AdminURL; want != "" && got != want {
		return nil, fmt.Errorf("this journal records an apply against %s but the configured "+
			"endpoint is %s; refusing to roll back against a different gateway", want, got)
	}

	// Only what actually changed needs undoing, in reverse order so a referent
	// is not restored before the thing that referenced it is dealt with.
	applied := appliedResults(j)
	if len(applied) == 0 {
		return &RollbackReport{
			Outcome: RollbackNothingToDo,
			Message: "this apply changed nothing; there is nothing to roll back",
		}, nil
	}

	report := &RollbackReport{Outcome: RollbackSucceeded}

	// Pre-flight: refuse if anything moved since the apply, unless forced.
	if !opts.Force {
		var moved []string
		for _, r := range applied {
			if drift := driftedSince(ctx, opts.Gateway, j, r); drift != "" {
				moved = append(moved, drift)
			}
		}
		if len(moved) > 0 {
			sort.Strings(moved)
			report.Outcome = RollbackBlocked
			report.Message = fmt.Sprintf("%d resource(s) have changed since this apply, so "+
				"rolling back would overwrite work done afterwards. Nothing was changed. "+
				"Inspect them, then re-run with --force if restoring is still what you want.",
				len(moved))
			report.ManualSteps = moved
			report.Err = errors.New("state changed since the apply")
			return report, nil
		}
	}

	var w *journal.Writer
	if opts.JournalPath != "" {
		var err error
		if w, err = journal.Create(opts.JournalPath); err != nil {
			return nil, err
		}
		defer w.Close() //nolint:errcheck // entries are synced individually
		_ = w.Append(journal.Entry{Type: journal.EntryHeader, Header: &journal.Header{
			FormatVersion: journal.FormatVersion, StartedAt: now(),
			PlanDigest: j.Header.PlanDigest, AdminURL: j.Header.AdminURL,
			KeyFingerprint: opts.KeyFingerprint, OperationCount: len(applied),
		}})
	}

	for _, r := range applied {
		item := rollbackOne(ctx, opts.Gateway, j, r)
		report.Items = append(report.Items, item)

		if item.Done {
			report.Restored++
		} else {
			report.Failed++
			report.ManualSteps = append(report.ManualSteps,
				fmt.Sprintf("%s/%s: %s", item.Kind, item.ID, item.Message))
		}

		if w != nil {
			outcome := journal.OutcomeApplied
			if !item.Done {
				outcome = journal.OutcomeFailed
			}
			_ = w.Append(journal.Entry{Type: journal.EntryResult, Result: &journal.Result{
				Type: "rollback:" + item.Action, Kind: item.Kind, ID: item.ID,
				Outcome: outcome, Error: item.Message, At: now(),
			}})
		}
	}

	switch {
	case report.Failed == 0:
		report.Message = fmt.Sprintf("%d resource(s) restored", report.Restored)
	case report.Restored == 0:
		report.Outcome = RollbackManualRequired
		report.Message = "nothing could be restored automatically; see the manual steps below"
		report.Err = errors.New("rollback failed")
	default:
		report.Outcome = RollbackPartial
		report.Message = fmt.Sprintf("%d resource(s) restored, %d could not be. Live state is "+
			"still mixed; see the manual steps below.", report.Restored, report.Failed)
		report.Err = errors.New("rollback incomplete")
	}

	if w != nil {
		_ = w.Append(journal.Entry{Type: journal.EntryFooter, Footer: &journal.Footer{
			FinishedAt: now(), Applied: report.Restored, Failed: report.Failed,
			Message: report.Message,
		}})
	}

	return report, report.Err
}

// appliedResults returns the operations that changed state, newest first.
//
// An uncertain result is included: the change may have taken effect, so it must
// be considered for restoration rather than assumed not to have happened.
func appliedResults(j *journal.Journal) []journal.Result {
	var out []journal.Result
	for _, r := range j.Results {
		if r.Outcome == journal.OutcomeApplied || r.Outcome == journal.OutcomeUncertain {
			out = append(out, r)
		}
	}
	// Reverse: undo in the opposite order to the one things were done in.
	for i, k := 0, len(out)-1; i < k; i, k = i+1, k-1 {
		out[i], out[k] = out[k], out[i]
	}
	return out
}

// driftedSince reports whether a resource changed after the apply touched it.
func driftedSince(ctx context.Context, g Gateway, j *journal.Journal, r journal.Result) string {
	def, ok := model.Lookup(r.Kind)
	if !ok {
		return fmt.Sprintf("%s/%s: unknown kind", r.Kind, r.ID)
	}

	current, err := g.Get(ctx, def, r.ID)
	if apisix.IsKind(err, apisix.ErrNotFound) {
		// Deleted by the apply, or by someone since. Either way there is
		// nothing here that a rollback would overwrite.
		return ""
	}
	if err != nil {
		return fmt.Sprintf("%s/%s: could not be read: %v", r.Kind, r.ID, err)
	}

	// The apply recorded the revision it produced. Anything else means someone
	// wrote after it.
	if r.ModifiedIndexAfter != 0 && current.ModifiedIndex != r.ModifiedIndexAfter {
		return fmt.Sprintf("%s/%s was modified after the apply (revision %d, apply left %d)",
			r.Kind, r.ID, current.ModifiedIndex, r.ModifiedIndexAfter)
	}
	return ""
}

// rollbackOne restores a single resource.
func rollbackOne(ctx context.Context, g Gateway, j *journal.Journal, r journal.Result) RollbackItem {
	item := RollbackItem{Kind: r.Kind, ID: r.ID}

	def, ok := model.Lookup(r.Kind)
	if !ok {
		item.Message = fmt.Sprintf("unknown kind %q", r.Kind)
		return item
	}

	before := j.BeforeFor(r.Kind, r.ID)
	if before == nil {
		item.Message = "the journal has no record of this resource's prior state, so it cannot " +
			"be restored automatically"
		return item
	}

	// The resource did not exist before, so undoing means removing it.
	if !before.Existed {
		item.Action = "delete"
		if err := g.Delete(ctx, def, r.ID); err != nil {
			if apisix.IsKind(err, apisix.ErrNotFound) {
				item.Done = true
				item.Message = "already absent"
				return item
			}
			item.Message = fmt.Sprintf("could not remove the resource this apply created: %v", err)
			return item
		}
		item.Done = true
		return item
	}

	// Restoring a prior state SixSync only holds redacted would replace real
	// credentials with a placeholder. Refuse and say exactly what is needed.
	if len(before.RedactedFields) > 0 {
		item.Action = "restore"
		item.Message = fmt.Sprintf("its prior state contains credential field(s) that the "+
			"journal deliberately does not store (%v), so SixSync cannot restore it without "+
			"replacing real values with placeholders. Restore this resource from your "+
			"repository or from a secret store", before.RedactedFields)
		return item
	}

	item.Action = "restore"
	if _, err := g.Put(ctx, def, r.ID, before.State); err != nil {
		item.Message = fmt.Sprintf("could not restore the prior state: %v", err)
		return item
	}

	// Confirm the restoration actually landed.
	restored, err := g.Get(ctx, def, r.ID)
	if err != nil {
		item.Message = fmt.Sprintf("the restore was accepted but reading it back failed: %v", err)
		return item
	}
	if before.Digest != "" {
		canon, cErr := canonical.Canonicalize(r.Kind, restored.Value)
		if cErr == nil {
			if digest, dErr := canonical.Digest(canon); dErr == nil && digest != before.Digest {
				item.Message = "the resource was written back but does not match its recorded " +
					"prior state; inspect it by hand"
				return item
			}
		}
	}

	item.Done = true
	return item
}
