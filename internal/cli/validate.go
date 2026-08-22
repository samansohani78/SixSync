package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/config"
	"github.com/samansohani78/SixSync/internal/exitcode"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/resource"
)

type validationResult struct {
	Path    string     `json:"path"`
	Kind    model.Kind `json:"kind"`
	ID      string     `json:"id"`
	Valid   bool       `json:"valid"`
	Message string     `json:"message,omitempty"`

	// RemoteChecked records whether APISIX was consulted, so a passing report
	// cannot be mistaken for more assurance than it carries.
	RemoteChecked bool `json:"remote_checked"`
}

func newValidateCommand(e *env) *cobra.Command {
	var remote bool

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate resource files, optionally against APISIX's own schemas",
		Long: "Checks that each file is structurally sound and can be canonicalized.\n\n" +
			"With --remote, each resource is additionally sent to APISIX's schema\n" +
			"validation endpoint, which validates its request body and stores nothing.\n" +
			"validate is therefore read-only either way.\n\n" +
			"Remote validation is structural only: APISIX does not check that referenced\n" +
			"resources exist, so passing validation does not guarantee a write would\n" +
			"succeed.",
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
			if len(repo.Errors) > 0 {
				for _, le := range repo.Errors {
					fmt.Fprintf(e.err, "cannot read %s\n", le)
				}
				return exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("%d file(s) could not be read; run `sixsync lint` for details",
						len(repo.Errors)))
			}

			var client *apisix.Client
			if remote {
				client, err = newClient(cfg)
				if err != nil {
					return err
				}
			}

			results := make([]validationResult, 0, len(repo.Resources))
			for _, l := range repo.Resources {
				r := validateOne(cmd, client, l, remote)

				// APISIX's schema endpoint is reached with POST, and a key
				// with role: viewer is refused any non-GET method. That is a
				// property of the credential, not of the resource, so it must
				// not be reported as every file being invalid.
				if r.RemoteChecked && !r.Valid && isViewerRefusal(r.Message) {
					return exitcode.Wrap(exitcode.Usage, errors.New(
						"remote validation needs a key that may use POST, but this key has "+
							"role: viewer. APISIX exposes schema validation only through a "+
							"write method, even though it changes nothing. Run without "+
							"--remote, or supply an administrator key"))
				}
				results = append(results, r)
			}
			sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })

			if err := reportValidate(e, cfg, results, remote); err != nil {
				return err
			}
			for _, r := range results {
				if !r.Valid {
					return exitcode.Wrap(exitcode.Failure, errors.New("validation failed"))
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&remote, "remote", false,
		"also validate against APISIX's schema endpoint (read-only)")
	return cmd
}

func validateOne(cmd *cobra.Command, client *apisix.Client, l resource.Loaded, remote bool) validationResult {
	res := validationResult{
		Path: l.Path, Kind: l.File.Kind, ID: l.File.Metadata.ID, Valid: true,
	}

	// A tombstone carries no spec, so there is nothing to validate beyond the
	// structural checks the loader already applied.
	if l.File.Lifecycle.State == resource.StateAbsent {
		return res
	}

	// Canonicalization is itself a real check: it fails on shapes SixSync
	// cannot compare, which are exactly the ones that would produce a
	// misleading diff later.
	canonSpec, err := canonical.Canonicalize(l.File.Kind, l.File.Spec)
	if err != nil {
		res.Valid, res.Message = false, err.Error()
		return res
	}

	if !remote || client == nil {
		return res
	}

	def, ok := model.Lookup(l.File.Kind)
	if !ok {
		res.Valid, res.Message = false, fmt.Sprintf("unknown kind %s", l.File.Kind)
		return res
	}

	// APISIX validates the body as submitted, so send the file's spec rather
	// than the canonical form, whose reference-field coercion exists only for
	// comparison.
	_ = canonSpec

	// Several kinds — global_rule and plugin_config among them — declare `id`
	// as a required property, so the schema endpoint rejects a body without
	// it. SixSync keeps identity in metadata rather than spec, so it is put
	// back just for this call. The file on disk is unchanged.
	body := make(map[string]any, len(l.File.Spec)+1)
	for k, v := range l.File.Spec {
		body[k] = v
	}
	body[def.IdentityField] = l.File.Metadata.ID

	res.RemoteChecked = true
	if err := client.ValidateSchema(cmd.Context(), def, body); err != nil {
		res.Valid, res.Message = false, err.Error()
	}
	return res
}

func reportValidate(e *env, cfg config.Config, results []validationResult, remote bool) error {
	if cfg.OutputFormat == "json" {
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"remote_validation": remote,
			"results":           results,
		})
	}

	var failed int
	for _, r := range results {
		if r.Valid {
			continue
		}
		failed++
		fmt.Fprintf(e.out, "invalid  %s: %s\n", r.Path, r.Message)
	}

	if failed == 0 {
		fmt.Fprintf(e.out, "%d resource(s) valid.\n", len(results))
	} else {
		fmt.Fprintf(e.out, "\n%d of %d resource(s) invalid.\n", failed, len(results))
	}
	if !remote {
		fmt.Fprintln(e.out, "Local checks only. Pass --remote to also validate against APISIX's schemas.")
	}
	return nil
}

// isViewerRefusal reports whether a failure came from the credential's role
// rather than from the resource being invalid.
func isViewerRefusal(msg string) bool {
	return strings.Contains(msg, string(apisix.ErrAuthInsufficient))
}
