package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/config"
	"github.com/samansohani78/SixSync/internal/exitcode"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/resource"
)

// importResult records what happened to one resource, for the report.
type importResult struct {
	Kind    model.Kind `json:"kind"`
	ID      string     `json:"id"`
	Path    string     `json:"path"`
	Written bool       `json:"written"`
	Skipped string     `json:"skipped,omitempty"`
}

func newImportCommand(e *env) *cobra.Command {
	var (
		dryRun    bool
		overwrite bool
		kinds     []string
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Read live APISIX resources into files",
		Long: "Reads resources from the Admin API and writes one file per resource.\n\n" +
			"APISIX identifiers are preserved exactly, so an existing gateway can be\n" +
			"adopted without recreating resources and interrupting traffic.\n\n" +
			"Existing files are never overwritten unless --overwrite is given: an import\n" +
			"onto a populated directory would otherwise discard local edits that have not\n" +
			"been applied yet.\n\n" +
			"Kinds that carry credentials are refused rather than imported. Run\n" +
			"`sixsync doctor` to see which kinds those are and why.",
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

			selected, err := selectKinds(kinds)
			if err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}

			results, err := runImport(cmd.Context(), client, cfg, selected, dryRun, overwrite)
			if err != nil {
				return err
			}
			return reportImport(e, cfg, results, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"report what would be written without touching the filesystem")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false,
		"replace existing files (discards local edits)")
	cmd.Flags().StringSliceVar(&kinds, "kind", nil,
		"limit to these kinds (default: every supported kind)")
	return cmd
}

// selectKinds resolves the --kind filter, refusing unsupported kinds by name
// so that a user asking for one gets a reason rather than an empty result.
func selectKinds(names []string) ([]model.Definition, error) {
	if len(names) == 0 {
		return model.Supported(), nil
	}

	var out []model.Definition
	for _, n := range names {
		def, ok := model.Lookup(model.Kind(n))
		if !ok {
			return nil, fmt.Errorf("unknown kind %q", n)
		}
		if !def.Supported {
			return nil, fmt.Errorf("kind %s cannot be imported: %s", def.Kind, def.RefusalReason)
		}
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

func runImport(
	ctx context.Context,
	client *apisix.Client,
	cfg config.Config,
	kinds []model.Definition,
	dryRun, overwrite bool,
) ([]importResult, error) {
	var results []importResult

	for _, def := range kinds {
		remote, err := client.List(ctx, def)
		if err != nil {
			return nil, exitcode.Wrap(exitcode.Failure,
				fmt.Errorf("listing %s: %w", def.Kind, err))
		}

		for _, r := range remote {
			id, err := r.ID(def)
			if err != nil {
				return nil, exitcode.Wrap(exitcode.Failure, err)
			}

			f, err := toFile(def, id, r)
			if err != nil {
				return nil, exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("converting %s %s: %w", def.Kind, id, err))
			}

			path := filepath.Join(cfg.RepoDir, def.APIPath, resource.FileName(f))
			res := importResult{Kind: def.Kind, ID: id, Path: path}

			if !overwrite {
				if _, statErr := os.Stat(path); statErr == nil {
					res.Skipped = "file already exists; pass --overwrite to replace it"
					results = append(results, res)
					continue
				}
			}

			if dryRun {
				results = append(results, res)
				continue
			}

			data, err := resource.Encode(f)
			if err != nil {
				return nil, exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("encoding %s %s: %w", def.Kind, id, err))
			}
			// 0o600: an imported file describes a production gateway's routing.
			if err := resource.WriteAtomic(path, data, 0o600); err != nil {
				return nil, exitcode.Wrap(exitcode.Failure, err)
			}

			res.Written = true
			results = append(results, res)
		}
	}

	return results, nil
}

// toFile converts a live APISIX resource into a SixSync resource file.
//
// The identity field and the server-owned timestamps are lifted out of the
// body: identity belongs in metadata, and APISIX rejects a write that carries
// create_time or update_time. Everything else is preserved verbatim, including
// fields this build of SixSync does not recognize.
func toFile(def model.Definition, id string, r apisix.Resource) (*resource.File, error) {
	spec := make(map[string]any, len(r.Value))
	for k, v := range r.Value {
		spec[k] = v
	}
	delete(spec, def.IdentityField)
	delete(spec, "create_time")
	delete(spec, "update_time")

	name, _ := spec["name"].(string)

	f := &resource.File{
		APIVersion: resource.APIVersion,
		Kind:       def.Kind,
		Metadata:   resource.Metadata{ID: id, Name: name},
		Lifecycle:  resource.Lifecycle{State: resource.StatePresent},
		Spec:       spec,
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return f, nil
}

func reportImport(e *env, cfg config.Config, results []importResult, dryRun bool) error {
	if cfg.OutputFormat == "json" {
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"dry_run":   dryRun,
			"resources": results,
		})
	}

	var written, skipped int
	for _, r := range results {
		switch {
		case r.Skipped != "":
			skipped++
			fmt.Fprintf(e.out, "skip   %-14s %-24s %s (%s)\n", r.Kind, r.ID, r.Path, r.Skipped)
		case dryRun:
			fmt.Fprintf(e.out, "would  %-14s %-24s %s\n", r.Kind, r.ID, r.Path)
		default:
			written++
			fmt.Fprintf(e.out, "write  %-14s %-24s %s\n", r.Kind, r.ID, r.Path)
		}
	}

	if len(results) == 0 {
		fmt.Fprintln(e.out, "No resources found.")
		return nil
	}

	if dryRun {
		fmt.Fprintf(e.out, "\n%d resource(s) would be written, %d skipped. Nothing was changed.\n",
			len(results)-skipped, skipped)
		return nil
	}
	fmt.Fprintf(e.out, "\n%d resource(s) written, %d skipped.\n", written, skipped)
	return nil
}
