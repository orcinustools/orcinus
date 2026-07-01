package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/biznetgio/orcinus/pkg/deploy"
)

func newPsCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:   "ps <project>",
		Short: "List the pods of an orcinus-managed project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := deploy.LoadRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			applier, err := deploy.NewApplier(cfg)
			if err != nil {
				return err
			}
			pods, err := applier.ListProjectPods(cmd.Context(), args[0], namespace)
			if err != nil {
				return err
			}
			if len(pods) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no pods found for project %q\n", args[0])
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "SERVICE\tPOD\tREADY\tSTATUS\tRESTARTS\tNODE")
			for _, p := range pods {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", p.Service, p.Name, p.Ready, p.Status, p.Restarts, p.Node)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace (default: all namespaces)")
	return cmd
}
