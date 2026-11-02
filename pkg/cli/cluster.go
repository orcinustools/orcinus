package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/cluster"
)

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
		newClusterUpdateCmd(),
		newDownCmd(),
		newNetfixCmd(),
	)
	return cmd
}

// newNetfixCmd is the hidden helper behind the cross-host VXLAN fix: init/join
// copy the orcinus binary into the node container and run this command there
// (the k3s image ships no ethtool).
func newNetfixCmd() *cobra.Command {
	var wait time.Duration
	cmd := &cobra.Command{
		Use:    "netfix [iface]",
		Hidden: true,
		Short:  "Disable VXLAN TX checksum offload on an interface (runs inside a node container)",
		Args:   cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			iface := "flannel.1"
			if len(args) > 0 {
				iface = args[0]
			}
			return cluster.DisableVXLANChecksumOffload(iface, wait)
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 5*time.Minute, "how long to wait for the interface to appear")
	return cmd
}

// newClusterUpdateCmd changes a running cluster's ingress ports — the ones
// `cluster init --http-port/--https-port` would have published.
func newClusterUpdateCmd() *cobra.Command {
	var name string
	var httpPort, httpsPort int
	var upgrade bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Change a running cluster's ingress ports, or upgrade its Kubernetes version",
		Long: "Publish (or stop publishing) the ingress ports of an existing cluster, and/or\n" +
			"upgrade it to the Kubernetes version this orcinus release ships (--upgrade).\n\n" +
			"--upgrade steps the control plane one minor version at a time (Kubernetes\n" +
			"cannot skip minors), waiting for the node to come up Ready on each; workers\n" +
			"follow. Run it on the server host first, then on each host that joined.\n\n" +
			"Docker cannot add ports to a running container, so the node container is\n" +
			"recreated on the same volumes and hostname: workloads, secrets and the join\n" +
			"token stay, and the API is down for the few seconds of the restart. If the\n" +
			"new container does not come up, the old one is put back.",
		Example: "  orcinus cluster update --http-port 80 --https-port 443\n  orcinus cluster update --https-port 0   # stop publishing 443\n" +
			"  orcinus cluster update --upgrade",
		RunE: func(cmd *cobra.Command, _ []string) error {
			o := cluster.UpdateOptions{Name: name}
			if cmd.Flags().Changed("http-port") {
				o.HTTPPort = &httpPort
			}
			if cmd.Flags().Changed("https-port") {
				o.HTTPSPort = &httpsPort
			}
			if o.HTTPPort == nil && o.HTTPSPort == nil {
				if !upgrade {
					return fmt.Errorf("nothing to change: pass --http-port/--https-port and/or --upgrade")
				}
				v, err := cluster.Upgrade(name)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "cluster on %s\n", v)
				return nil
			}
			h, s, err := cluster.Update(o)
			if err != nil {
				return err
			}
			show := func(p int) string {
				if p == 0 {
					return "not published"
				}
				return fmt.Sprint(p)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ingress: http %s, https %s\n", show(h), show(s))
			if upgrade {
				v, err := cluster.Upgrade(name)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "cluster on %s\n", v)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "cluster name (default: from saved state)")
	f.IntVar(&httpPort, "http-port", 0, "publish ingress HTTP on this host port (0 = stop publishing)")
	f.IntVar(&httpsPort, "https-port", 0, "publish ingress HTTPS on this host port (0 = stop publishing)")
	f.BoolVar(&upgrade, "upgrade", false, "upgrade to Kubernetes "+cluster.TargetVersion()+", one minor version at a time")
	return cmd
}
