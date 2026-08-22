// Package cli defines SixSync's command tree.
//
// Commands here are thin: they resolve configuration, call into the packages
// that do the work, and render the result. Business logic lives elsewhere so it
// can be tested without going through a command line.
//
// No command in this build mutates remote state. Every Admin API call SixSync
// makes today is a GET or the schema-validation endpoint, which validates its
// request body and stores nothing.
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/config"
	"github.com/samansohani78/SixSync/internal/exitcode"
)

// env carries everything a command needs, so that tests can substitute streams
// instead of writing to the real stdout.
type env struct {
	out io.Writer
	err io.Writer

	// configPath is the --config flag.
	configPath string

	// cmd is retained so commands can read which flags were actually set.
	// A flag the user did not set must not mask the environment or the file.
	cmd *cobra.Command
}

// resolveConfig merges the configuration sources, passing on only those flags
// the user explicitly set.
func (e *env) resolveConfig() (config.Config, error) {
	set := map[string]string{}
	if e.cmd != nil {
		flags := e.cmd.Flags()
		for _, s := range config.Settings {
			if f := flags.Lookup(s.Flag); f != nil && f.Changed {
				set[s.Flag] = f.Value.String()
			}
		}
	}

	cfg, err := config.Resolve(set, e.configPath)
	if err != nil {
		return cfg, exitcode.Wrap(exitcode.Usage, err)
	}

	// Disabling verification makes the connection trivially interceptable, and
	// the Admin API key travels over it. Say so every time, on stderr so it
	// survives redirection of machine-readable output.
	if cfg.InsecureSkipVerify {
		fmt.Fprintln(e.err,
			"WARNING: TLS certificate verification is disabled. The connection can be "+
				"intercepted and the Admin API key read in transit. Do not use this against "+
				"any system you care about.")
	}
	return cfg, nil
}

// NewRootCommand builds the command tree.
func NewRootCommand(out, errOut io.Writer) *cobra.Command {
	e := &env{out: out, err: errOut}

	root := &cobra.Command{
		Use:   "sixsync",
		Short: "GitOps for Apache APISIX, through the Admin API",
		Long: "SixSync manages Apache APISIX configuration declaratively through the\n" +
			"Admin API, with Git as the source of truth.\n\n" +
			"This build is read-only: no command changes remote state.\n\n" +
			"SixSync is a community project. It is not affiliated with or endorsed by\n" +
			"the Apache Software Foundation.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Without a subcommand, print help rather than doing anything.
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) { e.cmd = cmd }

	root.PersistentFlags().StringVar(&e.configPath, "config", "",
		"path to a SixSync configuration file")

	// Global flags are generated from the settings table so that a new setting
	// cannot be added without its flag appearing.
	pf := root.PersistentFlags()
	for _, s := range config.Settings {
		switch s.Key {
		case "admin.insecure_skip_verify":
			pf.Bool(s.Flag, false, s.Usage)
		default:
			pf.String(s.Flag, "", s.Usage)
		}
	}

	root.AddCommand(
		newVersionCommand(e),
		newDoctorCommand(e),
		newImportCommand(e),
		newDriftCommand(e),
		newLintCommand(e),
		newValidateCommand(e),
	)
	return root
}

// Execute runs the command tree and returns the process exit code.
//
// This is the only place an error becomes an exit status, which keeps the
// exit-code contract in one reviewable location.
func Execute(args []string, out, errOut io.Writer) exitcode.Code {
	root := NewRootCommand(out, errOut)
	root.SetArgs(args)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return exitcode.From(err)
	}
	return exitcode.OK
}
