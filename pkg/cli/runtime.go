package cli

import (
	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/runtime"
)

// newRuntimeCmd is a hidden passthrough to the standalone Kubernetes runtime,
// making a single orcinus binary able to *be* the runtime as well as drive it:
//
//	orcinus runtime server [flags...]   # run the standalone runtime server
//	orcinus runtime kubectl <args...>   # the runtime's built-in kubectl
//	orcinus runtime agent  [flags...]
//
// Only functional in the standalone build (`make orcinus-standalone`); otherwise it
// returns a clear "not compiled in" error. It execs the runtime, replacing this
// process, so it never returns on success.
func newRuntimeCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "runtime [args...]",
		Short:              "Run the standalone Kubernetes runtime (passthrough)",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runtime.Exec(args)
		},
	}
}
