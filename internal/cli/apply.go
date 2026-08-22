package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/apply"
	"github.com/samansohani78/SixSync/internal/buildinfo"
	"github.com/samansohani78/SixSync/internal/exitcode"
	"github.com/samansohani78/SixSync/internal/plan"
	"github.com/samansohani78/SixSync/internal/redact"
	"github.com/samansohani78/SixSync/internal/resource"
)

func newApplyCommand(e *env) *cobra.Command {
	var (
		planPath      string
		journalPath   string
		approve       bool
		approveDelete bool
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Execute a previously generated plan",
		Long: "Applies the changes described by a plan artifact.\n\n" +
			"apply consumes a plan; it never computes one. The change that was reviewed is\n" +
			"the change that runs.\n\n" +
			"Before the first write, every affected resource is re-read and compared against\n" +
			"the revision the plan recorded. If anything moved, the whole run is abandoned\n" +
			"with the gateway untouched. Each write is then verified by reading it back, and\n" +
			"the first unrecoverable failure stops the run.\n\n" +
			"This is not atomic. APISIX has no transaction across resources, so a failure\n" +
			"partway through leaves some changes applied and some not. Everything that\n" +
			"happens is recorded in a journal, which `sixsync rollback` can use.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if planPath == "" {
				return exitcode.Wrap(exitcode.Usage,
					errors.New("--plan is required; generate one with `sixsync plan --out`"))
			}

			cfg, err := e.resolveConfig()
			if err != nil {
				return err
			}
			client, err := newClient(cfg)
			if err != nil {
				return err
			}

			f, err := os.Open(planPath) //nolint:gosec // G304: an operator-specified path is the point
			if err != nil {
				return exitcode.Wrap(exitcode.Usage, fmt.Errorf("opening the plan: %w", err))
			}
			p, err := plan.Read(f)
			closeErr := f.Close()
			if err != nil {
				return exitcode.Wrap(exitcode.Failure, err)
			}
			if closeErr != nil {
				return exitcode.Wrap(exitcode.Failure, closeErr)
			}

			repo, err := resource.Load(cfg.RepoDir)
			if err != nil {
				return exitcode.Wrap(exitcode.Failure, err)
			}
			if len(repo.Errors) > 0 {
				return exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("%d resource file(s) could not be read; the working tree must be "+
						"readable to apply a plan built from it", len(repo.Errors)))
			}

			if journalPath == "" {
				journalPath = defaultJournalPath(e.now())
			}

			report, runErr := apply.Run(cmd.Context(), apply.Options{
				Plan: p, Repo: repo, Gateway: client,
				JournalPath:    journalPath,
				Approved:       approve,
				DeleteApproved: approveDelete,
				KeyFingerprint: redact.Fingerprint(cfg.AdminKey),
				SixSyncVersion: buildinfo.Get().Version,
				Now:            e.now,
			})

			renderApplyReport(e, report)
			return applyExitCode(report, runErr)
		},
	}

	cmd.Flags().StringVar(&planPath, "plan", "", "path to the plan artifact to apply")
	cmd.Flags().StringVar(&journalPath, "journal", "",
		"where to write the execution journal (default: sixsync-journal-<timestamp>.jsonl)")
	cmd.Flags().BoolVar(&approve, "approve", false,
		"confirm that the plan has been reviewed and should be applied")
	cmd.Flags().BoolVar(&approveDelete, "approve-delete", false,
		"additionally confirm that deletions in the plan should proceed")
	return cmd
}

// defaultJournalPath names a journal after the run that produced it.
func defaultJournalPath(now time.Time) string {
	return fmt.Sprintf("sixsync-journal-%s.jsonl", now.UTC().Format("20060102T150405Z"))
}

func renderApplyReport(e *env, r *apply.Report) {
	if r == nil {
		return
	}

	switch r.Outcome {
	case apply.OutcomeNothingToDo:
		fmt.Fprintln(e.out, "No changes to apply.")
		return

	case apply.OutcomeStale:
		fmt.Fprintln(e.out, "Plan is stale. Nothing was changed.")
		for _, c := range r.Conflicts {
			fmt.Fprintf(e.out, "  %s\n", c)
		}
		fmt.Fprintf(e.out, "\n%s\n", r.Message)
		return
	}

	if r.JournalPath != "" {
		fmt.Fprintf(e.out, "Journal: %s\n", r.JournalPath)
	}
	fmt.Fprintf(e.out, "Applied %d, failed %d, skipped %d", r.Applied, r.Failed, r.Skipped)
	if r.Uncertain > 0 {
		fmt.Fprintf(e.out, ", UNCERTAIN %d", r.Uncertain)
	}
	fmt.Fprintln(e.out, ".")

	if r.Message != "" {
		fmt.Fprintf(e.out, "\n%s\n", r.Message)
	}
}

// applyExitCode maps an outcome onto the documented exit-code contract.
func applyExitCode(r *apply.Report, err error) error {
	if r == nil {
		return exitcode.Wrap(exitcode.Failure, err)
	}
	switch r.Outcome {
	case apply.OutcomeSucceeded, apply.OutcomeNothingToDo:
		return nil
	case apply.OutcomeStale:
		return exitcode.Wrap(exitcode.Conflict, errors.New("the plan is stale; nothing was changed"))
	case apply.OutcomePartial:
		// Distinct from a plain failure: live state is mixed and needs
		// attention, which is what code 5 signals.
		return exitcode.Wrap(exitcode.RollbackIncomplete,
			fmt.Errorf("apply stopped partway: %w", err))
	default:
		if err == nil {
			err = errors.New("apply failed")
		}
		if isPolicyDenial(err) {
			return exitcode.Wrap(exitcode.PolicyDenied, err)
		}
		return exitcode.Wrap(exitcode.Failure, err)
	}
}

func isPolicyDenial(err error) bool {
	return err != nil && err.Error() == "policy denied this plan"
}
