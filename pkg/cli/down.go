package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/biznetgio/orcinus/pkg/cluster"
)

func newDownCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop and remove the orcinus cluster",
		RunE: func(cmd *cobra.Command, _ []string) error {
			n, err := cluster.Down(name)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %d cluster container(s) and cleared local state\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "cluster name (default: from saved state)")
	return cmd
}
