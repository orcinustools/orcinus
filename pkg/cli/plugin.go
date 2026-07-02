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
	cmd.AddCommand(newPluginListCmd(), newPluginInfoCmd(), newPluginInstallCmd(), newPluginRemoveCmd())
	return cmd
}

func newPluginInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <name>",
		Short: "Show details about a plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, ok := plugin.Get(args[0])
			if !ok {
				return fmt.Errorf("unknown plugin %q (see `orcinus plugin list`)", args[0])
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "name:        %s\n", s.Name)
			fmt.Fprintf(out, "description: %s\n", s.Description)
			fmt.Fprintf(out, "installed:   %t\n", plugin.Installed(s.Name))
			if s.Version != "" {
				fmt.Fprintf(out, "version:     %s\n", s.Version)
			}
			if len(s.Providers) > 0 {
				fmt.Fprintf(out, "providers:   %v\n", s.Providers)
			}
			for i, m := range s.Manifests {
				if i == 0 {
					fmt.Fprintf(out, "manifests:   %s\n", m)
				} else {
					fmt.Fprintf(out, "             %s\n", m)
				}
			}
			if s.Notes != "" {
				fmt.Fprintf(out, "notes:       %s\n", s.Notes)
			}
			return nil
		},
	}
}

func newPluginRemoveCmd() *cobra.Command {
	var o plugin.Options
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed plugin from the cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := plugin.Remove(cmd.Context(), args[0], o); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "plugin %q removed\n", args[0])
			return nil
		},
	}
	pluginFlags(cmd, &o)
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
	var profile string
	cmd := &cobra.Command{
		Use:   "install [name]",
		Short: "Install a plugin (or a --profile set) into the cluster",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if profile != "" {
				if err := plugin.InstallProfile(cmd.Context(), profile, o); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "profile %q installed\n", profile)
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("provide a plugin name or --profile")
			}
			if err := plugin.Install(cmd.Context(), args[0], o); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "plugin %q installed\n", args[0])
			return nil
		},
	}
	pluginFlags(cmd, &o)
	cmd.Flags().StringVar(&profile, "profile", "", "install a profile set (web, observability)")
	return cmd
}

// pluginFlags registers the shared install/remove option flags.
func pluginFlags(cmd *cobra.Command, o *plugin.Options) {
	f := cmd.Flags()
	f.StringVar(&o.Kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	f.StringVar(&o.Email, "email", "", "ACME account email (cert-manager)")
	f.BoolVar(&o.Staging, "staging", false, "use Let's Encrypt staging (cert-manager)")
	f.StringVar(&o.Provider, "provider", "", "provider variant (storage: local-path|longhorn|nfs|minio|rook-ceph)")
	f.StringVar(&o.Size, "size", "", "volume size (e.g. 10Gi) — storage: minio")
	f.StringVar(&o.NFSServer, "nfs-server", "", "NFS server address (storage: nfs)")
	f.StringVar(&o.NFSPath, "nfs-path", "", "NFS export path (storage: nfs)")
	f.IntVar(&o.Replicas, "replicas", 0, "replica count — storage: minio (distributed), longhorn, rook-ceph pool size")
	f.StringVar(&o.CephDeviceFilter, "ceph-device-filter", "", "rook-ceph: device regex (e.g. '^sd[b-d]')")
	f.StringVar(&o.CephFailureDomain, "ceph-failure-domain", "", "rook-ceph: pool failure domain (host|osd|rack)")
}
