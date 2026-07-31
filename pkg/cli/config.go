package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

func newConfigCmd() *cobra.Command {
	var kubeconfig, namespace, format string
	var showSecrets bool

	cmd := &cobra.Command{
		Use:   "config [project]",
		Short: "Show a deployed project's configuration, or export it",
		Long: `Read back what a project actually looks like on the cluster.

The cluster is the source of truth, not the compose file, so this also shows
drift someone applied with kubectl. With no project, the current directory name
is used — the same default as ` + "`orcinus deploy`" + `.

Formats:
  summary   one row per service: kind, image, replicas, ports, volumes (default)
  k8s       the live manifests as a multi-document YAML stream, re-appliable
  orcinus   an orcinus.yml rebuilt from the deployed workloads (compose)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validated before touching the cluster, so a typo reports itself
			// instead of surfacing as a kubeconfig error.
			switch format {
			case "summary", "", "k8s", "kubernetes", "orcinus", "compose":
			default:
				return fmt.Errorf("unknown format %q: use summary, k8s, or orcinus", format)
			}

			project := ""
			if len(args) == 1 {
				project = args[0]
			} else if wd, err := os.Getwd(); err == nil {
				project = filepath.Base(wd)
			}

			cfg, err := deploy.LoadRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			applier, err := deploy.NewApplier(cfg)
			if err != nil {
				return err
			}
			pc, err := applier.ProjectConfig(cmd.Context(), project, namespace)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			switch format {
			case "summary", "":
				return pc.WriteSummary(out)
			case "k8s", "kubernetes":
				b, err := pc.RenderK8s(showSecrets)
				if err != nil {
					return err
				}
				_, err = out.Write(b)
				return err
			default: // "orcinus", "compose" — the switch above rejects anything else
				b, err := pc.RenderCompose()
				if err != nil {
					return err
				}
				_, err = out.Write(b)
				return err
			}
		},
	}

	f := cmd.Flags()
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	f.StringVarP(&namespace, "namespace", "n", "default", "namespace")
	f.StringVarP(&format, "format", "o", "summary", "output format: summary | k8s | orcinus")
	f.BoolVar(&showSecrets, "show-secrets", false, "include Secret values in k8s output instead of redacting them")
	return cmd
}
