package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/plugin"
)

func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage cluster add-ons (ingress, cert-manager, storage, …)",
	}
	cmd.AddCommand(newPluginListCmd(), newPluginInstallCmd())
	return cmd
}

func newPluginListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available plugins",
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "NAME\tINSTALLED\tDESCRIPTION")
			for _, s := range plugin.List() {
				mark := "no"
				if plugin.Installed(s.Name) {
					mark = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, mark, s.Description)
			}
			return w.Flush()
		},
	}
}

func newPluginInstallCmd() *cobra.Command {
	var o plugin.Options
	cmd := &cobra.Command{
		Use:   "install <name>",
		Short: "Install a plugin into the cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := plugin.Install(cmd.Context(), args[0], o); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "plugin %q installed\n", args[0])
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	f.StringVar(&o.Email, "email", "", "ACME account email (cert-manager)")
	f.BoolVar(&o.Staging, "staging", false, "use Let's Encrypt staging (cert-manager)")
	return cmd
}
