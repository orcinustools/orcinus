package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/mcp"
)

func newMCPCmd() *cobra.Command {
	var allowWrite, printConfig bool
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve orcinus over MCP for AI agents (Claude Desktop, Codex, Cursor, …)",
		Long: "Run an MCP (Model Context Protocol) server over stdio so any MCP-capable agent\n" +
			"can use orcinus as tools and read its skill recipes. Read-only by default;\n" +
			"pass --allow-write to enable cluster-changing tools (deploy, scale, rm, …).\n\n" +
			"Add it to a client (example — Claude Desktop's claude_desktop_config.json):\n" +
			"  orcinus mcp --config",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printConfig {
				fmt.Fprint(cmd.OutOrStdout(), mcpConfigSnippet(allowWrite))
				return nil
			}
			srv := mcp.New(kubeconfig, allowWrite)
			return srv.Serve(os.Stdin, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&allowWrite, "allow-write", false, "enable cluster-mutating tools (deploy, scale, rollback, rm)")
	cmd.Flags().BoolVar(&printConfig, "config", false, "print an example MCP client config and exit")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, ~/.kube/config)")
	return cmd
}

func mcpConfigSnippet(allowWrite bool) string {
	args := `"mcp"`
	if allowWrite {
		args = `"mcp", "--allow-write"`
	}
	return `# MCP client config (works for Claude Desktop, Codex, opencode, Cursor, …).
# Point "command" at the orcinus binary. Read-only unless you add --allow-write.

{
  "mcpServers": {
    "orcinus": {
      "command": "orcinus",
      "args": [` + args + `]
    }
  }
}
`
}
