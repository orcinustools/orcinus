package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/cluster"
)

func newStatusCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show orcinus cluster status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := cluster.Status(name)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			state := "stopped"
			if s.Running {
				state = "running"
			}
			fmt.Fprintf(out, "cluster:    %s (%s)\n", s.State.Name, state)
			fmt.Fprintf(out, "server:     %s\n", s.State.ServerURL)
			fmt.Fprintf(out, "kubeconfig: %s\n", s.State.KubeconfigPath)
			if s.Nodes != "" {
				fmt.Fprintf(out, "\n%s", s.Nodes)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "cluster name (default: from saved state)")
	return cmd
}
