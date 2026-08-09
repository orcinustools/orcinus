package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

// newExecCmd wires `orcinus exec`, the compose-shaped way into a running
// container: name a *service* and orcinus picks one of its pods, the way
// `orcinus logs` does. A pod name works too, so a line copied out of
// `orcinus ps` can be pasted straight in.
func newExecCmd() *cobra.Command {
	var kubeconfig, namespace, project, container, pod string
	var stdin, tty bool

	cmd := &cobra.Command{
		Use:   "exec <service> [flags] -- <command> [args...]",
		Short: "Run a command inside a service's container",
		Long: `Run a command inside the container of an orcinus-managed service.

The target is a compose service — orcinus picks one of its running pods — or an
exact pod name. Put the command after "--" so its own flags are not read as
orcinus flags.`,
		Example: `  # A shell in the web service
  orcinus exec -it web -- sh

  # One-shot command, output straight to the terminal
  orcinus exec web -- cat /etc/hosts

  # Pipe something in
  cat dump.sql | orcinus exec -i db -- psql -U postgres

  # A specific pod, and a specific container inside it
  orcinus exec -it --pod web-5d9f7c8b6-abcde -c sidecar -- sh`,
		Args: func(c *cobra.Command, args []string) error {
			_, _, err := splitExecArgs(c.ArgsLenAtDash(), args, pod)
			return err
		},
		RunE: func(c *cobra.Command, args []string) error {
			target, command, err := splitExecArgs(c.ArgsLenAtDash(), args, pod)
			if err != nil {
				return err
			}
			applier, err := newApplier(kubeconfig)
			if err != nil {
				return err
			}
			opts := deploy.ExecOptions{
				Target:    target,
				Pod:       pod,
				Project:   project,
				Namespace: namespace,
				Container: container,
				Command:   command,
				TTY:       tty,
				Stdout:    c.OutOrStdout(),
				Stderr:    c.ErrOrStderr(),
			}
			// A TTY is useless without a way to type into it, so -t implies -i.
			if stdin || tty {
				opts.Stdin = c.InOrStdin()
			}

			notifyEOL := "\n"
			if tty {
				in, ok := c.InOrStdin().(*os.File)
				if !ok || !term.IsTerminal(int(in.Fd())) {
					return fmt.Errorf("cannot allocate a TTY: stdin is not a terminal (drop -t, or run this from an interactive shell)")
				}
				restore, sizes, err := enterRawTerminal(in)
				if err != nil {
					return err
				}
				defer restore()
				opts.TerminalSizeQueue = sizes
				// In raw mode the terminal no longer maps \n to CRLF, so notes
				// written alongside the session need the carriage return.
				notifyEOL = "\r\n"
			}
			errOut := c.ErrOrStderr()
			opts.Notify = func(msg string) { fmt.Fprintf(errOut, "%s%s", msg, notifyEOL) }

			err = applier.Exec(c.Context(), opts)
			// A command that ran and exited non-zero is not an orcinus error:
			// pass the status through as our own and stay quiet, like a shell.
			if exitCodeOf(err) > 0 {
				c.SilenceErrors = true
			}
			return err
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", kubeconfigFlagHelp)
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().StringVar(&project, "project", "", "further scope the service lookup to a project")
	cmd.Flags().StringVar(&pod, "pod", "", "target this exact pod instead of resolving a service")
	cmd.Flags().StringVarP(&container, "container", "c", "", "container to enter (default: the pod's only/first container)")
	cmd.Flags().BoolVarP(&stdin, "stdin", "i", false, "keep stdin open")
	cmd.Flags().BoolVarP(&tty, "tty", "t", false, "allocate a TTY (implies --stdin)")
	return cmd
}

// splitExecArgs separates the target from the command to run. dash is cobra's
// ArgsLenAtDash: the number of arguments that appeared before `--`, or -1 when
// there was no `--` at all, in which case the first argument is the target.
func splitExecArgs(dash int, args []string, pod string) (string, []string, error) {
	split := dash
	if split < 0 {
		split = 1
		if pod != "" {
			// --pod already named the target, so everything is the command.
			split = 0
		}
		if split > len(args) {
			split = len(args)
		}
	}
	targets, command := args[:split], args[split:]

	switch {
	case pod != "" && len(targets) > 0:
		return "", nil, fmt.Errorf("name either a service argument or --pod, not both (got %q and --pod %q)", targets[0], pod)
	case pod == "" && len(targets) == 0:
		return "", nil, fmt.Errorf("missing service name: name the service (or pod) to run in, e.g. `orcinus exec web -- sh`")
	case len(targets) > 1:
		return "", nil, fmt.Errorf("too many arguments before the command: exec runs in one service, got %v — did you mean `orcinus exec %s -- %s`?",
			targets, targets[0], strings.Join(targets[1:], " "))
	case len(command) == 0:
		name := pod
		if len(targets) == 1 {
			name = targets[0]
		}
		return "", nil, fmt.Errorf("missing command: say what to run after `--`, e.g. `orcinus exec %s -- sh`", name)
	}

	if len(targets) == 1 {
		return targets[0], command, nil
	}
	return "", command, nil
}

// enterRawTerminal puts the local terminal in raw mode so keystrokes reach the
// remote shell unmangled, and returns a restore func plus a queue that reports
// window resizes to it.
func enterRawTerminal(in *os.File) (func(), remotecommand.TerminalSizeQueue, error) {
	fd := int(in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, nil, fmt.Errorf("put terminal in raw mode: %w", err)
	}
	q := &terminalSizeQueue{fd: fd, done: make(chan struct{})}
	restore := func() {
		close(q.done)
		_ = term.Restore(fd, state)
	}
	return restore, q, nil
}

// terminalSizeQueue reports the local window size to the remote TTY. It polls
// rather than watching SIGWINCH so the same code works on every platform; a
// quarter second of lag on a resize is not something anyone types through.
type terminalSizeQueue struct {
	fd   int
	last remotecommand.TerminalSize
	done chan struct{}
}

// Next blocks until the terminal size differs from the last one reported, and
// returns nil once the session is over. The first call returns immediately with
// the current size.
func (q *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	for {
		if w, h, err := term.GetSize(q.fd); err == nil && w > 0 && h > 0 {
			size := remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}
			if size != q.last {
				q.last = size
				return &size
			}
		}
		select {
		case <-q.done:
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// exitCodeOf reports the status a remote command exited with, or 0 when err is
// nil or is an orcinus-level failure rather than a command that ran and failed.
// client-go reports the former as an error carrying ExitStatus().
func exitCodeOf(err error) int {
	var coder interface{ ExitStatus() int }
	if err == nil || !errors.As(err, &coder) {
		return 0
	}
	if code := coder.ExitStatus(); code > 0 {
		return code
	}
	return 0
}

// ExitCode maps an error from the root command to a process exit status: the
// remote command's own status for `orcinus exec`, and 1 for everything else.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if code := exitCodeOf(err); code > 0 {
		return code
	}
	return 1
}
