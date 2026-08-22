package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/samansohani78/SixSync/internal/apisix"
	"github.com/samansohani78/SixSync/internal/exitcode"
	"github.com/samansohani78/SixSync/internal/model"
)

func newDoctorCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check connectivity, authentication and compatibility",
		Long: "Probes the configured Admin API endpoint and reports what SixSync found.\n\n" +
			"doctor never changes remote state. It issues reads, plus one request to the\n" +
			"schema-validation endpoint, which validates its request body and stores\n" +
			"nothing. That request is how doctor tells a viewer key from an administrator\n" +
			"key: APISIX checks the credential's role before dispatching, so a viewer key\n" +
			"is refused while an administrator key reaches the validator.",
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

			d := client.Diagnose(cmd.Context())

			if cfg.OutputFormat == "json" {
				enc := json.NewEncoder(e.out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(d); err != nil {
					return err
				}
			} else {
				writeDoctorText(e, d)
			}

			if !d.Reachable || !d.Authenticated {
				return exitcode.Wrap(exitcode.Failure,
					errors.New("the Admin API endpoint is not usable; see the report above"))
			}
			return nil
		},
	}
}

func writeDoctorText(e *env, d apisix.Diagnosis) {
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	unknownIfEmpty := func(s string) string {
		if s == "" {
			return "unknown"
		}
		return s
	}

	fmt.Fprintf(e.out, "Endpoint:            %s\n", d.Endpoint)
	fmt.Fprintf(e.out, "Reachable:           %s\n", yesNo(d.Reachable))
	fmt.Fprintf(e.out, "Authenticated:       %s\n", yesNo(d.Authenticated))
	fmt.Fprintf(e.out, "Key capability:      %s\n", d.Capability)
	fmt.Fprintf(e.out, "APISIX version:      %s\n", unknownIfEmpty(d.APISIXVersion))
	fmt.Fprintf(e.out, "Admin API version:   %s\n", unknownIfEmpty(d.AdminAPIVersion))
	fmt.Fprintf(e.out, "Schema validation:   %s\n", yesNo(d.SchemaValidationAvailable))

	fmt.Fprintf(e.out, "\nManaged resource kinds:\n")
	for _, def := range model.Supported() {
		fmt.Fprintf(e.out, "  %-14s %s\n", def.Kind, "read-only")
	}

	fmt.Fprintf(e.out, "\nNot managed:\n")
	for _, def := range model.All() {
		if def.Supported {
			continue
		}
		fmt.Fprintf(e.out, "  %-14s %s\n", def.Kind, def.RefusalReason)
	}

	if d.Capability == apisix.CapabilityAdmin {
		fmt.Fprintf(e.out, "\nNote: this key can write. SixSync issues no writes in this build,\n"+
			"but a read-only workflow should use a key with role: viewer.\n")
	}

	if len(d.Problems) > 0 {
		fmt.Fprintf(e.out, "\nProblems:\n")
		for _, p := range d.Problems {
			fmt.Fprintf(e.out, "  - %s\n", p)
		}
	}
}
