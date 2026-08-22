package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/canonical"
	"github.com/samansohani78/SixSync/internal/config"
	"github.com/samansohani78/SixSync/internal/exitcode"
	"github.com/samansohani78/SixSync/internal/model"
	"github.com/samansohani78/SixSync/internal/resource"
)

// driftKind classifies one difference.
type driftKind string

const (
	// driftChanged means both sides have the resource but they differ.
	driftChanged driftKind = "changed"

	// driftMissingRemote means a file describes a resource APISIX does not have.
	driftMissingRemote driftKind = "missing-remote"

	// driftUnmanaged means APISIX has a resource no file describes.
	//
	// Reported separately and never as something to delete. A resource with no
	// file is far more likely to be one SixSync does not manage yet than one an
	// operator wants removed.
	driftUnmanaged driftKind = "unmanaged"
)

type driftItem struct {
	Type driftKind  `json:"type"`
	Kind model.Kind `json:"kind"`
	ID   string     `json:"id"`
	Path string     `json:"path,omitempty"`

	// LocalDigest and RemoteDigest are canonical digests, safe to print: a
	// digest of a resource reveals nothing about a credential inside it.
	LocalDigest  string `json:"local_digest,omitempty"`
	RemoteDigest string `json:"remote_digest,omitempty"`
}

func newDriftCommand(e *env) *cobra.Command {
	var detailedExit bool

	cmd := &cobra.Command{
		Use:   "drift",
		Short: "Compare local resource files with live APISIX state",
		Long: "Reports where the gateway and the repository disagree.\n\n" +
			"drift changes nothing on either side. A resource present in APISIX but absent\n" +
			"from the repository is reported as unmanaged, never as something to delete.\n\n" +
			"Both sides are canonicalized before comparison, so differences in field\n" +
			"ordering, node encoding, reference types and server-set timestamps are not\n" +
			"reported as drift. See docs/compatibility.md for why each rule exists.",
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

			items, err := computeDrift(cmd.Context(), client, repo)
			if err != nil {
				return err
			}

			if err := reportDrift(e, cfg, items); err != nil {
				return err
			}

			// Differences are a normal, expected outcome, so they are only an
			// exit-code signal when the caller asks for one. A pipeline that
			// wants to gate on drift opts in; an operator running it by hand
			// does not get a non-zero status for a routine check.
			if detailedExit && len(items) > 0 {
				return exitcode.Wrap(exitcode.Diff, errors.New("differences found"))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&detailedExit, "detailed-exitcode", false,
		"exit with code 2 when differences are found")
	return cmd
}

func computeDrift(ctx context.Context, client *apisix.Client, repo *resource.Repo) ([]driftItem, error) {
	local := repo.ByKey()
	var items []driftItem

	for _, def := range model.Supported() {
		remote, err := client.List(ctx, def)
		if err != nil {
			return nil, exitcode.Wrap(exitcode.Failure, fmt.Errorf("listing %s: %w", def.Kind, err))
		}

		remoteByID := make(map[string]apisix.Resource, len(remote))
		for _, r := range remote {
			id, err := r.ID(def)
			if err != nil {
				return nil, exitcode.Wrap(exitcode.Failure, err)
			}
			remoteByID[id] = r
		}

		// Local side: changed, or missing remotely.
		for key, l := range local {
			if key.Kind != def.Kind {
				continue
			}
			// A tombstone asserts the resource should not exist. Its presence
			// remotely is a pending deletion, not drift, and this build has no
			// way to act on it.
			if l.File.Lifecycle.State == resource.StateAbsent {
				continue
			}

			r, present := remoteByID[key.ID]
			if !present {
				items = append(items, driftItem{
					Type: driftMissingRemote, Kind: def.Kind, ID: key.ID, Path: l.Path,
				})
				continue
			}

			localDigest, err := digestOf(def.Kind, l.File.Spec)
			if err != nil {
				return nil, exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("%s: %w", l.Path, err))
			}
			remoteDigest, err := digestOf(def.Kind, r.Value)
			if err != nil {
				return nil, exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("remote %s %s: %w", def.Kind, key.ID, err))
			}

			if localDigest != remoteDigest {
				items = append(items, driftItem{
					Type: driftChanged, Kind: def.Kind, ID: key.ID, Path: l.Path,
					LocalDigest: localDigest, RemoteDigest: remoteDigest,
				})
			}
		}

		// Remote side: resources no file describes.
		for id, r := range remoteByID {
			if _, present := local[resource.Key{Kind: def.Kind, ID: id}]; present {
				continue
			}
			d, err := digestOf(def.Kind, r.Value)
			if err != nil {
				return nil, exitcode.Wrap(exitcode.Failure,
					fmt.Errorf("remote %s %s: %w", def.Kind, id, err))
			}
			items = append(items, driftItem{
				Type: driftUnmanaged, Kind: def.Kind, ID: id, RemoteDigest: d,
			})
		}
	}

	// Output must be reproducible across runs.
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].Type != items[j].Type {
			return items[i].Type < items[j].Type
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func digestOf(kind model.Kind, spec map[string]any) (string, error) {
	c, err := canonical.Canonicalize(kind, spec)
	if err != nil {
		return "", err
	}
	return canonical.Digest(c)
}

func reportDrift(e *env, cfg config.Config, items []driftItem) error {
	if cfg.OutputFormat == "json" {
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"drift_count": len(items),
			"items":       items,
		})
	}

	if len(items) == 0 {
		fmt.Fprintln(e.out, "No differences. The repository matches live APISIX state.")
		return nil
	}

	for _, it := range items {
		switch it.Type {
		case driftChanged:
			fmt.Fprintf(e.out, "changed         %-14s %-24s %s\n", it.Kind, it.ID, it.Path)
		case driftMissingRemote:
			fmt.Fprintf(e.out, "missing-remote  %-14s %-24s %s\n", it.Kind, it.ID, it.Path)
		case driftUnmanaged:
			fmt.Fprintf(e.out, "unmanaged       %-14s %-24s (no local file)\n", it.Kind, it.ID)
		}
	}

	fmt.Fprintf(e.out, "\n%d difference(s). Nothing was changed.\n", len(items))
	fmt.Fprintln(e.out, "\"unmanaged\" means APISIX has a resource no file describes. SixSync never")
	fmt.Fprintln(e.out, "treats that as a reason to delete anything.")
	return nil
}
