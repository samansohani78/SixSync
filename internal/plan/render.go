package plan

import (
	"fmt"
	"io"
	"strings"

	"github.com/samansohani78/SixSync/internal/policy"
)

// Render writes a human-readable summary of a plan.
//
// The audience is someone deciding whether to approve a change, so the ordering
// is deliberate: what is blocked comes first, because a blocked plan cannot be
// applied at all; then policy; then the operations themselves; and the
// high-reach changes are called out rather than left for the reader to notice.
func Render(w io.Writer, p *Plan) {
	c := p.Content
	n := c.Summarize()

	fmt.Fprintf(w, "Plan for %s", c.Target.AdminURL)
	if c.Target.APISIXVersion != "" {
		fmt.Fprintf(w, " (APISIX %s)", c.Target.APISIXVersion)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Digest: %s\n\n", p.Digest)

	if blocked := c.Blocked(); len(blocked) > 0 {
		fmt.Fprintf(w, "BLOCKED — this plan cannot be applied:\n")
		for _, op := range blocked {
			fmt.Fprintf(w, "  %s/%s: %s\n", op.Kind, op.ID, op.BlockedReason)
		}
		fmt.Fprintln(w)
	}

	if c.Policy != nil && len(c.Policy.Violations) > 0 {
		fmt.Fprintf(w, "Policy (%s):\n", c.Policy.ProfileName)
		for _, v := range c.Policy.Violations {
			subject := v.Subject
			if subject == "" {
				subject = "(plan)"
			}
			fmt.Fprintf(w, "  %-5s %-30s [%s] %s\n", v.Outcome, subject, v.Rule, v.Message)
		}
		fmt.Fprintln(w)
	}

	for _, op := range c.Operations {
		if op.Status != StatusPlanned {
			continue
		}
		fmt.Fprintf(w, "%-7s %-14s %-28s %s\n", op.Type, op.Kind, op.ID, op.SourcePath)

		for _, ch := range op.Changes {
			switch ch.Type {
			case "added":
				fmt.Fprintf(w, "         + %s = %s\n", ch.Path, ch.New)
			case "removed":
				fmt.Fprintf(w, "         - %s (was %s)\n", ch.Path, ch.Old)
			default:
				fmt.Fprintf(w, "         ~ %s: %s -> %s\n", ch.Path, ch.Old, ch.New)
			}
		}

		if op.Impact != nil && op.Impact.Total > 0 {
			fmt.Fprintf(w, "         affects %d other resource(s)", op.Impact.Total)
			if len(op.Impact.DirectlyAffected) > 0 {
				shown := op.Impact.DirectlyAffected
				suffix := ""
				if len(shown) > 5 {
					shown, suffix = shown[:5], fmt.Sprintf(", and %d more",
						len(op.Impact.DirectlyAffected)-5)
				}
				fmt.Fprintf(w, ": %s%s", strings.Join(shown, ", "), suffix)
			}
			fmt.Fprintln(w)
		}

		for _, warning := range op.Warnings {
			fmt.Fprintf(w, "         ! %s\n", warning)
		}
	}

	if !c.HasChanges() && len(c.Blocked()) == 0 {
		fmt.Fprintln(w, "No changes. The gateway already matches the repository.")
	} else {
		fmt.Fprintf(w, "\n%d to create, %d to update, %d to delete, %d unchanged",
			n.Create, n.Update, n.Delete, n.Unchanged)
		if n.Blocked > 0 {
			fmt.Fprintf(w, ", %d blocked", n.Blocked)
		}
		fmt.Fprintln(w, ".")
	}

	if len(c.Unmanaged) > 0 {
		fmt.Fprintf(w, "\n%d resource(s) on the gateway are not described by any file:\n",
			len(c.Unmanaged))
		shown := c.Unmanaged
		suffix := 0
		if len(shown) > 10 {
			suffix = len(shown) - 10
			shown = shown[:10]
		}
		for _, u := range shown {
			fmt.Fprintf(w, "  %s/%s\n", u.Kind, u.ID)
		}
		if suffix > 0 {
			fmt.Fprintf(w, "  ... and %d more\n", suffix)
		}
		fmt.Fprintln(w, "SixSync never treats an undescribed resource as a reason to delete anything.")
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Nothing was changed. This is a plan.")
}

// Denied reports whether policy blocks this plan.
func (c Content) Denied() bool {
	return c.Policy != nil && c.Policy.Outcome == policy.OutcomeDeny
}
