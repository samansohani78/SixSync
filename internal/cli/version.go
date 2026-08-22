package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/buildinfo"
)

func newVersionCommand(e *env) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := buildinfo.Get()

			if format == "json" {
				enc := json.NewEncoder(e.out)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}

			fmt.Fprintf(e.out, "sixsync %s\n", info.Version)
			fmt.Fprintf(e.out, "  commit:   %s\n", info.Commit)
			fmt.Fprintf(e.out, "  built:    %s\n", info.Date)
			fmt.Fprintf(e.out, "  go:       %s\n", info.GoVersion)
			fmt.Fprintf(e.out, "  platform: %s\n", info.Platform)
			return nil
		},
	}

	// version deliberately takes its own --output rather than the global one:
	// it must work before any configuration exists, including before an
	// endpoint or credential has been set up.
	cmd.Flags().StringVarP(&format, "output", "o", "text", "output format: text or json")
	return cmd
}
