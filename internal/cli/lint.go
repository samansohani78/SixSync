package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/config"
	"github.com/samansohani78/SixSync/internal/exitcode"
	"github.com/samansohani78/SixSync/internal/lintrules"
	"github.com/samansohani78/SixSync/internal/resource"
)

func newLintCommand(e *env) *cobra.Command {
	var strict bool

	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Check resource files without contacting APISIX",
		Long: "Validates the repository on its own: file syntax, identifiers, duplicate\n" +
			"declarations, references between resources, and values that look like\n" +
			"credentials.\n\n" +
			"lint needs no Admin API endpoint and no credential, so it is safe to run on\n" +
			"every pull request, including from a fork.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := e.resolveConfig()
			if err != nil {
				return err
			}

			repo, err := resource.Load(cfg.RepoDir)
			if err != nil {
				return exitcode.Wrap(exitcode.Failure, err)
			}

			findings := lintrules.Check(repo)
			if err := reportLint(e, cfg, repo, findings); err != nil {
				return err
			}

			// Unparseable files are always fatal: SixSync cannot reason about
			// a repository it cannot read.
			if len(repo.Errors) > 0 {
				return exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("%d file(s) could not be read", len(repo.Errors)))
			}
			if lintrules.HasErrors(findings) {
				return exitcode.Wrap(exitcode.Failure, errors.New("lint found errors"))
			}
			if strict && len(findings) > 0 {
				return exitcode.Wrap(exitcode.Failure,
					errors.New("lint found warnings and --strict is set"))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as failures")
	return cmd
}

func reportLint(e *env, cfg config.Config, repo *resource.Repo, findings []lintrules.Finding) error {
	if cfg.OutputFormat == "json" {
		unreadable := make([]map[string]string, 0, len(repo.Errors))
		for _, le := range repo.Errors {
			unreadable = append(unreadable, map[string]string{
				"path": le.Path, "error": le.Err.Error(),
			})
		}
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"files_checked":    len(repo.Resources),
			"unreadable_files": unreadable,
			"findings":         findings,
		})
	}

	for _, le := range repo.Errors {
		fmt.Fprintf(e.out, "error    %s: %s\n", le.Path, le.Err)
	}
	for _, f := range findings {
		fmt.Fprintf(e.out, "%-8s %s: [%s] %s\n", f.Severity, f.Path, f.Rule, f.Message)
	}

	fmt.Fprintf(e.out, "\n%d file(s) checked", len(repo.Resources))
	if len(repo.Errors) > 0 {
		fmt.Fprintf(e.out, ", %d unreadable", len(repo.Errors))
	}
	fmt.Fprintf(e.out, ", %d finding(s).\n", len(findings))
	return nil
}
