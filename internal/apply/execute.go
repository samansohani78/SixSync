package apply

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/journal"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/plan"
	"github.com/samansohani78/SixSync/internal/redact"
)

// executeOne performs a single operation and verifies it.
//
// It never retries. A write whose response was lost may or may not have been
// applied; repeating it could overwrite a concurrent change that landed in
// between, and the honest outcome is "uncertain", which stops the run and tells
// the operator to look.
func executeOne(
	ctx context.Context,
	g Gateway,
	op plan.Operation,
	desired map[string]map[string]any,
	now func() time.Time,
) journal.Result {
	res := journal.Result{
		Order: op.Order, Type: string(op.Type), Kind: op.Kind, ID: op.ID, At: now(),
	}

	def, ok := model.Lookup(op.Kind)
	if !ok {
		res.Outcome = journal.OutcomeFailed
		res.Error = fmt.Sprintf("unknown kind %q", op.Kind)
		return res
	}
	// A refused kind must never be written, even if a plan somehow names one.
	if !def.Supported {
		res.Outcome = journal.OutcomeFailed
		res.Error = fmt.Sprintf("kind %s is not supported: %s", def.Kind, def.RefusalReason)
		return res
	}

	switch op.Type {
	case plan.OpDelete:
		return executeDelete(ctx, g, def, op, res, now)
	case plan.OpCreate, plan.OpUpdate:
		return executeWrite(ctx, g, def, op, desired, res, now)
	default:
		res.Outcome = journal.OutcomeFailed
		res.Error = fmt.Sprintf("unknown operation type %q", op.Type)
		return res
	}
}

func executeWrite(
	ctx context.Context,
	g Gateway,
	def model.Definition,
	op plan.Operation,
	desired map[string]map[string]any,
	res journal.Result,
	now func() time.Time,
) journal.Result {
	spec, ok := desired[opKey(op)]
	if !ok {
		res.Outcome = journal.OutcomeFailed
		res.Error = "the plan references a resource that is no longer in the repository; " +
			"the working tree changed after the plan was built"
		return res
	}

	// A redaction placeholder must never be written to the gateway. It would
	// replace a real credential with the literal string "[REDACTED]", breaking
	// authentication in a way that looks like a configuration change.
	if path := findPlaceholder(spec, nil); path != "" {
		res.Outcome = journal.OutcomeFailed
		res.Error = fmt.Sprintf("spec.%s holds a redaction placeholder; writing it would replace "+
			"a real value with %q. Supply the real value or remove the field", path,
			redact.Placeholder)
		return res
	}

	if _, err := g.Put(ctx, def, op.ID, spec); err != nil {
		res.Outcome = outcomeFor(err)
		res.Error = err.Error()
		res.At = now()
		return res
	}

	// Verify by reading back. A write APISIX accepted but stored differently —
	// or did not store at all — is a failure that would otherwise be reported
	// as success.
	written, err := g.Get(ctx, def, op.ID)
	if err != nil {
		res.Outcome = journal.OutcomeVerifyFailed
		res.Error = fmt.Sprintf("the write was accepted but reading it back failed: %v", err)
		res.At = now()
		return res
	}

	if err := verifyMatches(op.Kind, spec, written.Value); err != nil {
		res.Outcome = journal.OutcomeVerifyFailed
		res.Error = err.Error()
		res.At = now()
		return res
	}

	res.Outcome = journal.OutcomeApplied
	res.ModifiedIndexAfter = written.ModifiedIndex
	res.At = now()
	return res
}

func executeDelete(
	ctx context.Context,
	g Gateway,
	def model.Definition,
	op plan.Operation,
	res journal.Result,
	now func() time.Time,
) journal.Result {
	if err := g.Delete(ctx, def, op.ID); err != nil {
		// APISIX refusing a delete because something still references the
		// resource is information worth surfacing plainly.
		if apisix.IsKind(err, apisix.ErrReferenced) {
			res.Outcome = journal.OutcomeFailed
			res.Error = fmt.Sprintf("APISIX refused the deletion because another resource still "+
				"references it: %v", err)
			res.At = now()
			return res
		}
		res.Outcome = outcomeFor(err)
		res.Error = err.Error()
		res.At = now()
		return res
	}

	// Verify it is actually gone.
	if _, err := g.Get(ctx, def, op.ID); err == nil {
		res.Outcome = journal.OutcomeVerifyFailed
		res.Error = "the delete was accepted but the resource is still present"
		res.At = now()
		return res
	} else if !apisix.IsKind(err, apisix.ErrNotFound) {
		res.Outcome = journal.OutcomeVerifyFailed
		res.Error = fmt.Sprintf("could not confirm the deletion: %v", err)
		res.At = now()
		return res
	}

	res.Outcome = journal.OutcomeApplied
	res.At = now()
	return res
}

// outcomeFor classifies a failed mutation.
//
// The distinction that matters is whether the request definitely did not take
// effect, or whether that is unknown. A transport failure after the request was
// sent is the second case, and must never be reported as the first.
func outcomeFor(err error) journal.Outcome {
	var ae *apisix.Error
	if errors.As(err, &ae) {
		switch ae.Kind {
		case apisix.ErrTransport:
			// The request may have been received and applied before the
			// connection failed. Nothing here can distinguish the two.
			return journal.OutcomeUncertain
		case apisix.ErrServer:
			// A 5xx means the server rejected it, which is a definite failure
			// — except that some proxies return 5xx after forwarding. Treated
			// as failed, since the response did arrive.
			return journal.OutcomeFailed
		default:
			return journal.OutcomeFailed
		}
	}
	return journal.OutcomeUncertain
}

// verifyMatches confirms the gateway stored what was sent.
//
// Comparison is canonical, so APISIX's own normalization — reordering keys,
// adding timestamps — does not register as a mismatch, while a field that was
// silently dropped or altered does.
func verifyMatches(kind model.Kind, sent, stored map[string]any) error {
	sentCanon, err := canonical.Canonicalize(kind, sent)
	if err != nil {
		return fmt.Errorf("cannot verify the write: %w", err)
	}
	storedCanon, err := canonical.Canonicalize(kind, stored)
	if err != nil {
		return fmt.Errorf("cannot verify the write: %w", err)
	}

	sentDigest, err := canonical.Digest(sentCanon)
	if err != nil {
		return err
	}
	storedDigest, err := canonical.Digest(storedCanon)
	if err != nil {
		return err
	}
	if sentDigest == storedDigest {
		return nil
	}

	// Name the differing fields, redacted, so an operator can act on it.
	changes := canonical.Redact(
		canonical.DiffFields(sentCanon, storedCanon),
		pathRedactor{policy: model.RedactionPolicy(kind)},
	)
	var fields []string
	for i, c := range changes {
		if i == 5 {
			fields = append(fields, fmt.Sprintf("and %d more", len(changes)-5))
			break
		}
		fields = append(fields, c.Path)
	}
	return fmt.Errorf("the gateway stored something different from what was sent; "+
		"differing field(s): %s", strings.Join(fields, ", "))
}

// pathRedactor adapts the kind registry's policy to the diff redactor.
type pathRedactor struct{ policy *redact.Policy }

func (p pathRedactor) RedactPath(path string) (string, bool) {
	if p.policy == nil {
		return "", false
	}
	clean := stripIndices(path)
	if p.policy.MatchesPath(strings.Split(clean, ".")) {
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

// findPlaceholder reports the first path holding a redaction placeholder.
func findPlaceholder(v any, path []string) string {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if found := findPlaceholder(val, append(append([]string{}, path...), k)); found != "" {
				return found
			}
		}
	case []any:
		for _, val := range t {
			if found := findPlaceholder(val, path); found != "" {
				return found
			}
		}
	case string:
		if t == redact.Placeholder {
			return joinDots(path)
		}
	}
	return ""
}
