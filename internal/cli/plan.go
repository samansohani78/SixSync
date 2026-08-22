package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/buildinfo"
	"github.com/samansohani78/SixSync/internal/config"
	"github.com/samansohani78/SixSync/internal/exitcode"
	"github.com/samansohani78/SixSync/internal/plan"
	"github.com/samansohani78/SixSync/internal/policy"
	"github.com/samansohani78/SixSync/internal/redact"
	"github.com/samansohani78/SixSync/internal/resource"
)

func newPlanCommand(e *env) *cobra.Command {
	var (
		outPath       string
		policyPath    string
		detailedExit  bool
		approveDelete bool
	)

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Work out what would change, without changing anything",
		Long: "Compares the repository with live APISIX state and writes a plan artifact\n" +
			"describing exactly what would change.\n\n" +
			"plan is read-only. It issues only reads, and the artifact it produces is what\n" +
			"`sixsync apply` consumes — apply will not act on anything else, so the change\n" +
			"that gets reviewed is the change that gets made.\n\n" +
			"The artifact is deterministic: the same repository against the same gateway\n" +
			"state always produces the same content and the same digest. When it was\n" +
			"generated and by which build are recorded separately, so they cannot disturb\n" +
			"it.\n\n" +
			"A plan is bound to the gateway it was built against and records the revision\n" +
			"of every resource it touches, so apply can refuse a plan that has gone stale.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := e.resolveConfig()
			if err != nil {
				return err
			}
			client, err := newClient(cfg)
			if err != nil {
				return err
			}

			repo, err := resource.Load(cfg.RepoDir)
			if err != nil {
				return exitcode.Wrap(exitcode.Failure, err)
			}
			if len(repo.Errors) > 0 {
				for _, le := range repo.Errors {
					fmt.Fprintf(e.err, "cannot read %s\n", le)
				}
				return exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("%d resource file(s) could not be read; "+
						"run `sixsync lint` for details", len(repo.Errors)))
			}

			profile, err := loadProfile(policyPath)
			if err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}

			// Discovering the version costs one read and makes the artifact
			// self-describing, which matters when a plan outlives the session.
			diag := client.Diagnose(cmd.Context())

			p, err := plan.Build(cmd.Context(), plan.Options{
				Repo:   repo,
				Client: client,
				Target: plan.Target{
					AdminURL:       client.BaseURL(),
					APISIXVersion:  diag.APISIXVersion,
					KeyFingerprint: redact.Fingerprint(cfg.AdminKey),
				},
				Profile:        profile,
				DeleteApproved: approveDelete,
			})
			if err != nil {
				return exitcode.Wrap(exitcode.Failure, err)
			}

			p.Meta = plan.Meta{
				GeneratedAt:    e.now(),
				SixSyncVersion: buildinfo.Get().Version,
				SixSyncCommit:  buildinfo.Get().Commit,
			}

			if outPath != "" {
				if err := writePlanFile(outPath, p); err != nil {
					return exitcode.Wrap(exitcode.Failure, err)
				}
				fmt.Fprintf(e.err, "Plan written to %s\n", outPath)
			}

			if err := emitPlan(e, cfg, p); err != nil {
				return err
			}

			// Ordering matters: a blocked plan is a failure regardless of the
			// exit-code flag, and policy denial is its own distinct signal.
			if blocked := p.Content.Blocked(); len(blocked) > 0 {
				return exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("%d operation(s) are blocked; this plan cannot be applied",
						len(blocked)))
			}
			if p.Content.Denied() {
				return exitcode.Wrap(exitcode.PolicyDenied, errors.New("policy denied this plan"))
			}
			if detailedExit && p.Content.HasChanges() {
				return exitcode.Wrap(exitcode.Diff, errors.New("changes found"))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&outPath, "out", "",
		"write the plan artifact to this path (required for `sixsync apply`)")
	cmd.Flags().StringVar(&policyPath, "policy", "",
		"path to a policy profile")
	cmd.Flags().BoolVar(&detailedExit, "detailed-exitcode", false,
		"exit with code 2 when there are changes to make")
	cmd.Flags().BoolVar(&approveDelete, "approve-delete", false,
		"record deletion approval, for profiles that require it")
	return cmd
}

// loadProfile reads a policy profile, falling back to the default.
func loadProfile(path string) (*policy.Profile, error) {
	if path == "" {
		return policy.DefaultProfile(), nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: an operator-specified path is the point
	if err != nil {
		return nil, fmt.Errorf("reading policy profile: %w", err)
	}
	return policy.ParseProfile(data)
}

// writePlanFile writes the artifact atomically, so an interrupted run cannot
// leave a truncated plan that apply would then refuse to parse.
func writePlanFile(path string, p *plan.Plan) error {
	var buf writeBuffer
	if err := plan.Write(&buf, p); err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	// 0600: a plan describes a production gateway's configuration in detail.
	return resource.WriteAtomic(path, buf.Bytes(), 0o600)
}

func emitPlan(e *env, cfg config.Config, p *plan.Plan) error {
	if cfg.OutputFormat == "json" {
		return plan.Write(e.out, p)
	}
	plan.Render(e.out, p)
	return nil
}

// writeBuffer is a minimal io.Writer collecting bytes, avoiding a bytes.Buffer
// import purely for this.
type writeBuffer struct{ b []byte }

func (w *writeBuffer) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
func (w *writeBuffer) Bytes() []byte { return w.b }
