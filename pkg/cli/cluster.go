package cli

import "github.com/spf13/cobra"

// newClusterCmd groups the cluster-lifecycle commands under `orcinus cluster`.
// Running `orcinus cluster` (or `--help`) lists its subcommands.
func newClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage the orcinus cluster (init, join, status, down)",
		Long:  "Manage the orcinus cluster lifecycle.\n\nRun a subcommand, e.g. `orcinus cluster init`.",
	}
	cmd.AddCommand(
		newInitCmd(),
		newJoinCmd(),
		newStatusCmd(),
		newDownCmd(),
	)
	return cmd
}
