package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/orcinustools/orcinus/pkg/monitor"
	"github.com/orcinustools/orcinus/pkg/plugin"
	"github.com/spf13/cobra"
)

func newMonitorCmd() *cobra.Command {
	var kubeconfig, namespace, service string
	var since time.Duration
	var follow, asJSON bool
	cmd := &cobra.Command{
		Use:   "monitor [exec|files]",
		Short: "See commands run and files changed inside containers (docs/MONITORING.md)",
		Long: "Show what runs inside containers, as seen by the kernel on every node:\n" +
			"commands executed, `kubectl exec` sessions, and files written, truncated,\n" +
			"deleted or chmod-ed.\n\n" +
			"  orcinus monitor enable              install the node agents (Tetragon)\n" +
			"  orcinus monitor                     everything from the last hour\n" +
			"  orcinus monitor exec -s web -f      commands run in service web, live\n" +
			"  orcinus monitor files --since 24h   file changes of the last day\n" +
			"  orcinus monitor block-shell         kill shells started in app containers",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"exec", "files"},
		RunE: func(cmd *cobra.Command, args []string) error {
			f := monitor.Filter{Namespace: namespace, Service: service}
			if len(args) == 1 {
				if args[0] != "exec" && args[0] != "files" {
					return fmt.Errorf("unknown event kind %q (exec or files)", args[0])
				}
				f.Kind = args[0]
			}
			out := cmd.OutOrStdout()
			print := func(e monitor.Event) { printEvent(out, e) }
			if asJSON {
				enc := json.NewEncoder(out)
				print = func(e monitor.Event) { _ = enc.Encode(e) }
			}
			return monitor.Read(cmd.Context(), kubeconfig, since, follow, f, print)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "only this namespace (default: all)")
	cmd.Flags().StringVarP(&service, "service", "s", "", "only this service")
	cmd.Flags().DurationVar(&since, "since", time.Hour, "how far back to look (0 = everything the agents still hold)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new events")
	cmd.Flags().BoolVar(&asJSON, "json", false, "one JSON object per event")
	cmd.AddCommand(newMonitorEnableCmd(), newMonitorDisableCmd(), newMonitorBlockShellCmd(), newMonitorStatusCmd())
	return cmd
}

func printEvent(w io.Writer, e monitor.Event) {
	what := e.Binary
	if e.Args != "" {
		what += " " + e.Args
	}
	switch e.Kind {
	case "session":
		what = "a shell or command was started from outside (kubectl/orcinus exec)"
	case "blocked":
		what = e.Path + "  by " + e.Binary + "  (killed by block-shell)"
	case "write", "truncate", "delete":
		what = e.Path + "  by " + e.Binary
	case "chmod":
		what = e.Path + " → " + e.Mode + "  by " + e.Binary
	}
	who := e.Namespace + "/" + e.Pod
	if e.Container != "" && e.Service != "" && e.Container != e.Service {
		who += "/" + e.Container
	}
	what = strings.ReplaceAll(what, "\n", `\n`) // one event, one line
	fmt.Fprintf(w, "%s  %-40s %-8s uid=%-5d %s\n", e.Time.Local().Format("2006-01-02 15:04:05"), who, e.Kind, e.UID, what)
}

func newMonitorEnableCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Install the node agents (the tetragon plugin) and the file-change policy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := plugin.Install(cmd.Context(), "tetragon", plugin.Options{Kubeconfig: kubeconfig}); err != nil {
				return err
			}
			// The agents load their eBPF programs after the install returns.
			for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(3 * time.Second) {
				s, err := monitor.GetStatus(cmd.Context(), kubeconfig)
				if err == nil && s.Agents > 0 && s.Ready == s.Agents {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("the node agents are not ready after 3m: kubectl -n %s get pods", plugin.TetragonNamespace)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "runtime monitoring is on; see events with: orcinus monitor -f")
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	return cmd
}

func newMonitorDisableCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Remove the node agents, shell blocking and the recorded events",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := monitor.BlockShell(cmd.Context(), kubeconfig, false, nil, nil); err != nil {
				return err
			}
			if err := plugin.Remove(cmd.Context(), "tetragon", plugin.Options{Kubeconfig: kubeconfig}); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "runtime monitoring is off")
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	return cmd
}

func newMonitorBlockShellCmd() *cobra.Command {
	var kubeconfig string
	var projects, namespaces []string
	var off bool
	cmd := &cobra.Command{
		Use:   "block-shell",
		Short: "Kill any shell or package manager started in a container orcinus deployed",
		Long: "Kill, in the kernel, any process that starts a shell or a package manager\n" +
			"inside a container orcinus deployed — whether the app spawns it (a remote\n" +
			"code execution) or someone runs `kubectl exec`. The app itself keeps running.\n\n" +
			"Blocked: " + strings.Join(monitor.Blocked, " ") + "\n\n" +
			"Pods not deployed by orcinus (the cluster's own, plugins) are never touched.\n" +
			"Run it again with other --project/--namespace to change the scope.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := monitor.BlockShell(cmd.Context(), kubeconfig, !off, projects, namespaces); err != nil {
				return err
			}
			switch {
			case off:
				fmt.Fprintln(cmd.OutOrStdout(), "shell blocking is off")
			default:
				scope := "every orcinus service"
				if len(projects) > 0 {
					scope = "projects " + strings.Join(projects, ", ")
				}
				if len(namespaces) > 0 {
					scope += " in namespaces " + strings.Join(namespaces, ", ")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "shell blocking is on for %s; attempts show in: orcinus monitor exec\n", scope)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "only these projects (repeatable)")
	cmd.Flags().StringSliceVarP(&namespaces, "namespace", "n", nil, "only these namespaces (repeatable)")
	cmd.Flags().BoolVar(&off, "off", false, "stop blocking")
	return cmd
}

func newMonitorStatusCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the node agents and what is being watched or blocked",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := monitor.GetStatus(cmd.Context(), kubeconfig)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "agents:        %d/%d nodes ready\n", s.Ready, s.Agents)
			fmt.Fprintf(w, "file changes:  %s\n", onOff(slices.Contains(s.Policies, "orcinus-file-changes")))
			fmt.Fprintf(w, "shell block:   %s\n", onOff(slices.Contains(s.Policies, monitor.BlockShellPolicy)))
			if s.Ready < s.Agents {
				fmt.Fprintln(w, "warning: nodes without a ready agent are not watched")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	return cmd
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
