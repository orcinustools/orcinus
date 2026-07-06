package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/skills"
)

func newSkillsCmd() *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "skills [name]",
		Short: "Agent-oriented usage recipes (run this to learn how to drive orcinus)",
		Long: "Built-in, task-oriented recipes so an AI agent can learn to use orcinus.\n\n" +
			"  orcinus skills                # list recipes\n" +
			"  orcinus skills <name>         # one recipe\n" +
			"  orcinus skills --all          # the whole catalog (read once, learn everything)\n" +
			"  orcinus skills --json         # machine-readable\n" +
			"  orcinus skills init --agent all   # install into your agent tool(s)",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(skills.List())
			}
			if all {
				_, err := fmt.Fprint(out, skills.All())
				return err
			}
			if len(args) == 1 {
				c, ok := skills.Get(args[0])
				if !ok {
					return fmt.Errorf("unknown skill %q (run `orcinus skills` to list)", args[0])
				}
				fmt.Fprintf(out, "# %s — %s\n\n%s\n", c.Name, c.Description, c.Body)
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "SKILL\tDESCRIPTION")
			for _, c := range skills.List() {
				name := c.Name
				if c.Danger {
					name += " ⚠"
				}
				fmt.Fprintf(w, "%s\t%s\n", name, c.Description)
			}
			_ = w.Flush()
			fmt.Fprintln(out, "\nRun `orcinus skills <name>` for a recipe, or `orcinus skills init --agent all` to install into your agent.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "print the full catalog")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	cmd.AddCommand(newSkillsInitCmd())
	return cmd
}

// agentManifest is the compact orcinus usage block injected into agent rule files.
func agentManifest() string {
	var names []string
	for _, c := range skills.List() {
		names = append(names, c.Name)
	}
	return "This project can be deployed with **orcinus** — it runs `docker-compose.yml`\n" +
		"files (and raw Kubernetes manifests) on a Kubernetes cluster.\n\n" +
		"Quickstart:\n" +
		"- Preview (no changes): `orcinus deploy -f <file> --dry-run`\n" +
		"- Deploy: `orcinus deploy -f docker-compose.yml --wait`\n" +
		"- Inspect: `orcinus ls` · `orcinus ps <project>` · `orcinus logs <service> -f`\n" +
		"- Remove: `orcinus rm <project>` (destructive) · `orcinus cluster down` (destructive)\n\n" +
		"Learn the full capability set at any time (self-describing):\n" +
		"- `orcinus skills` — list recipes\n" +
		"- `orcinus skills <name>` — one recipe\n" +
		"- `orcinus skills --all` — everything\n\n" +
		"Recipes: " + strings.Join(names, ", ") + ".\n\n" +
		"Safety: prefer `--dry-run` before applying; treat `rm` / `cluster down` as destructive."
}

const (
	blockBegin = "<!-- orcinus:begin -->"
	blockEnd   = "<!-- orcinus:end -->"
)

func newSkillsInitCmd() *cobra.Command {
	var agent, dir string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install the orcinus skill into your agent tool(s)",
		Long: "Write the orcinus usage guide into an agent's native format so it can use orcinus.\n" +
			"--agent: claude | codex | opencode | cursor | windsurf | aider | generic | all",
		RunE: func(cmd *cobra.Command, _ []string) error {
			targets := map[string]bool{}
			switch agent {
			case "all":
				for _, a := range []string{"claude", "cursor", "windsurf", "aider", "generic"} {
					targets[a] = true
				}
			case "codex", "opencode", "generic":
				targets["generic"] = true // all use AGENTS.md
			case "claude", "cursor", "windsurf", "aider":
				targets[agent] = true
			default:
				return fmt.Errorf("unknown --agent %q (want: claude|codex|opencode|cursor|windsurf|aider|generic|all)", agent)
			}

			body := agentManifest()
			var written []string
			for t := range targets {
				path, err := writeAgentSkill(dir, t, body)
				if err != nil {
					return err
				}
				written = append(written, path)
			}
			out := cmd.OutOrStdout()
			for _, p := range written {
				fmt.Fprintf(out, "wrote %s\n", p)
			}
			fmt.Fprintln(out, "\nFor tool-calling agents (Claude Desktop, Codex, opencode, Cursor, …) run an MCP\nserver instead: `orcinus mcp` (see `orcinus mcp --help` for the config snippet).")
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "all", "target agent: claude|codex|opencode|cursor|windsurf|aider|generic|all")
	cmd.Flags().StringVar(&dir, "dir", ".", "project directory to write into")
	return cmd
}

// writeAgentSkill writes/updates the orcinus guide for one agent target and
// returns the path written.
func writeAgentSkill(dir, target, body string) (string, error) {
	switch target {
	case "claude":
		p := filepath.Join(dir, ".claude", "skills", "orcinus", "SKILL.md")
		content := "---\nname: orcinus\ndescription: Deploy docker-compose files and Kubernetes manifests to a cluster with the orcinus CLI. Use when the repo has a docker-compose.yml / orcinus.yml, or the user asks to deploy, scale, expose, or manage a cluster.\n---\n\n" + body + "\n"
		return p, writeFileMkdir(p, content)
	case "cursor":
		p := filepath.Join(dir, ".cursor", "rules", "orcinus.mdc")
		content := "---\ndescription: Deploy with the orcinus CLI (docker-compose → Kubernetes)\nalwaysApply: false\n---\n\n" + body + "\n"
		return p, writeFileMkdir(p, content)
	case "windsurf":
		return injectBlock(filepath.Join(dir, ".windsurfrules"), body)
	case "aider":
		return injectBlock(filepath.Join(dir, "CONVENTIONS.md"), body)
	case "generic": // AGENTS.md — Codex, opencode, and the emerging cross-tool standard
		return injectBlock(filepath.Join(dir, "AGENTS.md"), body)
	}
	return "", fmt.Errorf("unknown target %q", target)
}

func writeFileMkdir(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// injectBlock inserts (or replaces) an orcinus-delimited section in a shared file,
// preserving the rest — so it's idempotent and coexists with the user's content.
func injectBlock(path, body string) (string, error) {
	block := blockBegin + "\n## Using orcinus\n\n" + body + "\n" + blockEnd
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, writeFileMkdir(path, block+"\n")
		}
		return "", err
	}
	s := string(existing)
	if i := strings.Index(s, blockBegin); i >= 0 {
		if j := strings.Index(s, blockEnd); j > i {
			s = s[:i] + block + s[j+len(blockEnd):]
			return path, os.WriteFile(path, []byte(s), 0o644)
		}
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return path, os.WriteFile(path, []byte(s+"\n"+block+"\n"), 0o644)
}
