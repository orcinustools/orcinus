package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Inspect and label cluster nodes (for placement constraints)",
	}
	cmd.AddCommand(newNodeLsCmd(), newNodeLabelCmd())
	return cmd
}

func newNodeLsCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List cluster nodes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			nodes, err := a.ListNodes(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATUS\tROLES\tVERSION")
			for _, n := range nodes {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", n.Name, n.Status, n.Roles, n.Version)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	return cmd
}

func newNodeLabelCmd() *cobra.Command {
	var kubeconfig string
	var remove []string
	cmd := &cobra.Command{
		Use:   "label <node> KEY=VALUE [KEY=VALUE ...]",
		Short: "Add/update (or --rm) node labels — referenced by placement constraints",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			node := args[0]
			set := map[string]string{}
			for _, kv := range args[1:] {
				i := strings.IndexByte(kv, '=')
				if i <= 0 {
					return fmt.Errorf("invalid label %q (want KEY=VALUE)", kv)
				}
				set[kv[:i]] = kv[i+1:]
			}
			if len(set) == 0 && len(remove) == 0 {
				return fmt.Errorf("provide at least one KEY=VALUE or --rm KEY")
			}
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			if err := a.LabelNode(cmd.Context(), node, set, remove); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "node %q labeled (%d set, %d removed)\n", node, len(set), len(remove))
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringArrayVar(&remove, "rm", nil, "label key to remove (repeatable)")
	return cmd
}
