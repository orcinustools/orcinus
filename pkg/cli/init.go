package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/cluster"
)

func newInitCmd() *cobra.Command {
	o := cluster.InitOptions{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Start a single-node cluster on this machine",
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := cluster.Init(o)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "cluster %q is ready\n", res.Name)
			fmt.Fprintf(out, "kubeconfig: %s\n\n", res.KubeconfigPath)
			fmt.Fprintln(out, "Deploy your app:")
			fmt.Fprintln(out, "  orcinus deploy -f docker-compose.yml")
			fmt.Fprintln(out, "\nAdd a node (run on another host, or here for a local agent):")
			fmt.Fprintf(out, "  orcinus cluster join --server %s --token %s\n", res.ServerURL, res.Token)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Name, "name", cluster.DefaultName, "cluster/server name")
	f.StringVar(&o.Image, "image", cluster.DefaultImage, "cluster runtime image")
	f.IntVar(&o.APIPort, "port", 6443, "host port for the API server")
	f.StringVar(&o.BindAddress, "bind", "127.0.0.1", "host interface to publish the API port on (use 0.0.0.0 for all interfaces)")
	f.StringVar(&o.Advertise, "advertise", "", "address other nodes/clients use to reach this server (adds a TLS SAN; enables remote join)")
	f.StringVar(&o.Token, "token", "", "join token (default: auto-generated)")
	f.BoolVar(&o.ClusterInit, "cluster-init", false, "embedded etcd (HA mode)")
	f.StringVar(&o.DatastoreEndpoint, "datastore-endpoint", "", "external datastore (etcd/Postgres/MySQL)")
	f.StringVar(&o.KubeconfigPath, "kubeconfig", "", "where to write the kubeconfig (default: ~/.orcinus/kubeconfig)")
	return cmd
}
