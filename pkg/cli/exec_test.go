package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The command to run is whatever follows `--`; without one, the first argument
// is the target and the rest is the command.
func TestSplitExecArgs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		dash        int // cobra's ArgsLenAtDash: -1 when there was no `--`
		args        []string
		pod         string
		wantTarget  string
		wantCommand []string
	}{
		{name: "service then command", dash: 1, args: []string{"web", "sh"}, wantTarget: "web", wantCommand: []string{"sh"}},
		{
			name: "command keeps its own flags", dash: 1,
			args: []string{"web", "ls", "-la", "/tmp"}, wantTarget: "web", wantCommand: []string{"ls", "-la", "/tmp"},
		},
		{name: "no dash", dash: -1, args: []string{"web", "sh"}, wantTarget: "web", wantCommand: []string{"sh"}},
		{name: "pod flag, dash", dash: 0, args: []string{"sh"}, pod: "web-1", wantCommand: []string{"sh"}},
		{name: "pod flag, no dash", dash: -1, args: []string{"sh"}, pod: "web-1", wantCommand: []string{"sh"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, command, err := splitExecArgs(tc.dash, tc.args, tc.pod)
			if err != nil {
				t.Fatalf("split: %v", err)
			}
			if target != tc.wantTarget {
				t.Errorf("target = %q, want %q", target, tc.wantTarget)
			}
			if strings.Join(command, " ") != strings.Join(tc.wantCommand, " ") {
				t.Errorf("command = %v, want %v", command, tc.wantCommand)
			}
		})
	}
}

func TestSplitExecArgsErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		dash int
		args []string
		pod  string
		want string
	}{
		{name: "nothing at all", dash: -1, args: nil, want: "missing service name"},
		{name: "target but no command", dash: -1, args: []string{"web"}, want: "missing command"},
		{name: "dash but no command", dash: 1, args: []string{"web"}, want: "missing command"},
		{name: "pod but no command", dash: -1, args: nil, pod: "web-1", want: "missing command"},
		{name: "two targets", dash: 2, args: []string{"web", "db", "sh"}, want: "too many arguments"},
		{name: "target and pod", dash: 1, args: []string{"web", "sh"}, pod: "web-1", want: "not both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := splitExecArgs(tc.dash, tc.args, tc.pod)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// codeErr stands in for client-go's exec.CodeExitError: a command that ran and
// exited non-zero, as opposed to orcinus failing to run it.
type codeErr struct{ code int }

func (e codeErr) Error() string   { return fmt.Sprintf("command terminated with exit code %d", e.code) }
func (e codeErr) ExitStatus() int { return e.code }

// `orcinus exec` is a shell: the remote command's status is the tool's status.
func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "success", err: nil, want: 0},
		{name: "ordinary failure", err: errors.New("no cluster"), want: 1},
		{name: "remote exit status", err: codeErr{42}, want: 42},
		{name: "wrapped remote status", err: fmt.Errorf("exec web: %w", codeErr{2}), want: 2},
		// A zero status carried on an error is still a failure of some kind;
		// exiting 0 would tell a script the opposite.
		{name: "zero status on an error", err: codeErr{0}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// The flags people already know from docker/kubectl have to be spelled the same
// way here, shorthands included.
func TestExecFlags(t *testing.T) {
	cmd, _, err := NewRootCmd().Find([]string{"exec"})
	if err != nil {
		t.Fatalf("find exec: %v", err)
	}
	for _, name := range []string{"stdin", "tty", "container", "pod", "project", "namespace"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s not registered", name)
		}
	}
	if got := cmd.Flags().ShorthandLookup("t"); got == nil || got.Name != "tty" {
		t.Errorf("-t should be the tty shorthand, got %v", got)
	}
	if got := cmd.Flags().ShorthandLookup("i"); got == nil || got.Name != "stdin" {
		t.Errorf("-i should be the stdin shorthand, got %v", got)
	}
}
