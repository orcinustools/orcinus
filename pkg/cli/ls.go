package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

func newLsCmd() *cobra.Command {
	var kubeconfig, namespace string
	var allNamespaces bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List orcinus-managed projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := deploy.LoadRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			applier, err := deploy.NewApplier(cfg)
			if err != nil {
				return err
			}
			ns := namespace
			if allNamespaces {
				ns = ""
			}
			projects, err := applier.ListProjects(cmd.Context(), ns)
			if err != nil {
				return err
			}
			if len(projects) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no orcinus-managed projects found")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "PROJECT\tWORKLOADS\tREADY\tNAMESPACES")
			for _, p := range projects {
				fmt.Fprintf(w, "%s\t%d\t%d/%d\t%s\n", p.Name, p.Workloads, p.Ready, p.Workloads, strings.Join(p.Namespaces, ","))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to list (default: all namespaces)")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", true, "list across all namespaces")
	return cmd
}
