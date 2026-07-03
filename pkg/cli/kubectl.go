package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/cluster"
	"github.com/orcinustools/orcinus/pkg/deploy"
)

// newKubectlCmd is a passthrough escape hatch: it runs the host's `kubectl`
// against the orcinus cluster's kubeconfig, or falls back to the cluster
// container's bundled kubectl when kubectl isn't installed locally.
func newKubectlCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "kubectl [args...]",
		Short:              "Run kubectl against the orcinus cluster (passthrough)",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// An standalone-runtime cluster has no container and may have no host
			// kubectl; use the runtime's own built-in kubectl.
			if st, err := cluster.LoadState(); err == nil && st.Runtime == "standalone" {
				if bin, ok := cluster.StandaloneKubectl(); ok {
					argv := append([]string{bin, "kubectl"}, args...)
					c := exec.Command(argv[0], argv[1:]...)
					c.Env = append(os.Environ(), "KUBECONFIG="+st.KubeconfigPath)
					c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
					return c.Run()
				}
			}
			// Prefer a locally-installed kubectl.
			if bin, err := exec.LookPath("kubectl"); err == nil {
				c := exec.Command(bin, args...)
				c.Env = append(os.Environ(), "KUBECONFIG="+deploy.ResolveKubeconfigPath(""))
				c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
				return c.Run()
			}
			// Fall back to the orcinus cluster container's kubectl.
			st, err := cluster.LoadState()
			if err != nil {
				return fmt.Errorf("kubectl not found on PATH and no orcinus cluster; install kubectl or run `orcinus cluster init`")
			}
			docker := strings.Fields(dockerCmdEnv())
			argv := append(append(docker, "exec", "-i", st.Name, "kubectl"), args...)
			c := exec.Command(argv[0], argv[1:]...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
			return c.Run()
		},
	}
}

func dockerCmdEnv() string {
	if v := strings.TrimSpace(os.Getenv("ORCINUS_DOCKER")); v != "" {
		return v
	}
	return "docker"
}
