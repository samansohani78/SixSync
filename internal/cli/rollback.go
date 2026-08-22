package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/apply"
	"github.com/samansohani78/SixSync/internal/exitcode"
	"github.com/samansohani78/SixSync/internal/journal"
	"github.com/samansohani78/SixSync/internal/redact"
)

func newRollbackCommand(e *env) *cobra.Command {
	var (
		journalPath string
		outPath     string
		force       bool
	)

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Restore the resources an apply changed, using its journal",
		Long: "Undoes an apply, using the journal that apply wrote.\n\n" +
			"This is best effort, and the limits are real. It is not a transaction:\n" +
			"resources are restored one at a time, and a failure partway leaves a partial\n" +
			"restoration. It cannot restore a credential it never had, because journals are\n" +
			"redacted — those are reported as manual steps rather than written back as\n" +
			"placeholders. And it cannot undo effects: traffic that was routed was routed.\n\n" +
			"If anything has changed since the apply, rollback refuses rather than\n" +
			"overwriting work done afterwards. --force overrides that.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if journalPath == "" {
				return exitcode.Wrap(exitcode.Usage,
					errors.New("--journal is required; apply writes one on every run"))
			}

			cfg, err := e.resolveConfig()
			if err != nil {
				return err
			}
			client, err := newClient(cfg)
			if err != nil {
				return err
			}

			j, err := journal.Open(journalPath)
			if err != nil {
				return exitcode.Wrap(exitcode.Failure, err)
			}

			report, runErr := apply.Rollback(cmd.Context(), apply.RollbackOptions{
				Journal: j, Gateway: client, Force: force,
				JournalPath:    outPath,
				KeyFingerprint: redact.Fingerprint(cfg.AdminKey),
				Now:            e.now,
			})
			if report == nil {
				return exitcode.Wrap(exitcode.Failure, runErr)
			}

			renderRollbackReport(e, report)

			switch report.Outcome {
			case apply.RollbackSucceeded, apply.RollbackNothingToDo:
				return nil
			case apply.RollbackBlocked:
				return exitcode.Wrap(exitcode.Conflict,
					errors.New("state changed since the apply; nothing was rolled back"))
			default:
				return exitcode.Wrap(exitcode.RollbackIncomplete,
					errors.New("rollback did not complete; see the manual steps above"))
			}
		},
	}

	cmd.Flags().StringVar(&journalPath, "journal", "", "path to the journal an apply wrote")
	cmd.Flags().StringVar(&outPath, "out", "", "where to write the rollback's own journal")
	cmd.Flags().BoolVar(&force, "force", false,
		"roll back even though resources changed after the apply (overwrites later work)")
	return cmd
}

func renderRollbackReport(e *env, r *apply.RollbackReport) {
	for _, item := range r.Items {
		status := "restored"
		if !item.Done {
			status = "FAILED  "
		}
		fmt.Fprintf(e.out, "%s %-14s %-28s %s\n", status, item.Kind, item.ID, item.Message)
	}

	if len(r.Items) > 0 {
		fmt.Fprintln(e.out)
	}
	fmt.Fprintf(e.out, "%s\n", r.Message)

	if len(r.ManualSteps) > 0 {
		fmt.Fprintln(e.out, "\nThese need a person:")
		for _, s := range r.ManualSteps {
			fmt.Fprintf(e.out, "  - %s\n", s)
		}
	}
}
