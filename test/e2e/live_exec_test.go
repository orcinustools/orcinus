package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveExec drives `orcinus exec` against a real cluster.
//
// Unlike the other live tests it does not boot one. Point
// ORCINUS_E2E_KUBECONFIG at a cluster that is already running — a shared
// testing server, say — and the test works inside a namespace of its own, so
// whatever else is deployed there is never touched. The namespace is created
// and deleted by the test.
//
//	ORCINUS_E2E_LIVE        set to enable live e2e
//	ORCINUS_E2E_KUBECONFIG  kubeconfig of an existing cluster to test against
func TestLiveExec(t *testing.T) {
	requireLive(t)
	kubeconfig := os.Getenv("ORCINUS_E2E_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ORCINUS_E2E_KUBECONFIG to a running cluster's kubeconfig to run the exec e2e")
	}
	if _, err := os.Stat(kubeconfig); err != nil {
		t.Fatalf("ORCINUS_E2E_KUBECONFIG: %v", err)
	}

	ns := fmt.Sprintf("orcinus-e2e-exec-%d", time.Now().Unix())
	const project = "e2e-exec"

	// The cluster is selected through $KUBECONFIG rather than --kubeconfig: an
	// exec command line ends in `-- <cmd>`, and a trailing flag after that
	// belongs to the remote command, not to orcinus. HOME is redirected too, so
	// a stray ~/.orcinus/kubeconfig on the machine running the test cannot
	// quietly win instead (deploy.LoadRESTConfig prefers it over ~/.kube).
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home, "KUBECONFIG="+kubeconfig)

	// run returns stdout, stderr and the exit status separately: exec is
	// specified in terms of all three, so a helper that merges them would not
	// be able to tell most of these cases apart.
	run := func(args ...string) (stdout, stderr string, code int) {
		t.Helper()
		cmd := exec.Command(orcinusBin, args...)
		cmd.Dir = repoRoot()
		cmd.Env = env
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		if err != nil {
			var ee *exec.ExitError
			if !asExitError(err, &ee) {
				t.Fatalf("run %v: %v\nstderr: %s", args, err, errb.String())
			}
			code = ee.ExitCode()
		}
		return out.String(), errb.String(), code
	}
	orcinus := func(args ...string) (string, error) {
		stdout, stderr, code := run(args...)
		if code != 0 {
			return stdout + stderr, fmt.Errorf("exit %d", code)
		}
		return stdout + stderr, nil
	}
	// execOut runs an exec and fails unless it succeeded, returning trimmed stdout.
	execOut := func(args ...string) string {
		t.Helper()
		stdout, stderr, code := run(append([]string{"exec", "-n", ns}, args...)...)
		if code != 0 {
			t.Fatalf("orcinus exec %v: exit %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
		}
		return strings.TrimSpace(stdout)
	}

	if out, err := orcinus("kubectl", "create", "namespace", ns); err != nil {
		t.Fatalf("create namespace: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := orcinus("kubectl", "delete", "namespace", ns, "--wait=false"); err != nil {
			t.Logf("cleanup: delete namespace %s: %v\n%s", ns, err, out)
		}
	})

	// One compose service (the ordinary case, and the only one that carries the
	// service label exec resolves on) plus a raw two-container Deployment, which
	// compose cannot express but `-c` has to cope with. Both go in one deploy:
	// prune is scoped to the project, so a second deploy would remove the first.
	compose := writeTemp(t, "orcinus.yml", `
services:
  shell:
    image: busybox:1.36
    command: ["sh", "-c", "sleep 3600"]
`)
	// Each container reports a different WHO, so `-c` is checked against
	// something that actually differs between them — the pod's hostname is
	// shared by both and would pass no matter which one ran the command.
	duo := writeTemp(t, "duo.yaml", `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: duo
spec:
  replicas: 1
  selector:
    matchLabels: { app: duo }
  template:
    metadata:
      labels: { app: duo }
      annotations:
        kubectl.kubernetes.io/default-container: sidecar
    spec:
      containers:
        - name: app
          image: busybox:1.36
          command: ["sh", "-c", "sleep 3600"]
          env: [{ name: WHO, value: i-am-app }]
        - name: sidecar
          image: busybox:1.36
          command: ["sh", "-c", "sleep 3600"]
          env: [{ name: WHO, value: i-am-sidecar }]
`)
	if out, err := orcinus("deploy", "-f", compose, "-f", duo,
		"-n", ns, "--project", project, "--wait"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}

	shellPod := strings.TrimSpace(mustOrcinus(t, orcinus, "kubectl", "get", "pod", "-n", ns,
		"-l", "io.kompose.service=shell", "-o", "jsonpath={.items[0].metadata.name}"))
	duoPod := strings.TrimSpace(mustOrcinus(t, orcinus, "kubectl", "get", "pod", "-n", ns,
		"-l", "app=duo", "-o", "jsonpath={.items[0].metadata.name}"))
	if shellPod == "" || duoPod == "" {
		t.Fatalf("could not find the deployed pods (shell=%q duo=%q)", shellPod, duoPod)
	}

	// --- how the target is resolved ---
	t.Run("by service name", func(t *testing.T) {
		if got := execOut("shell", "--", "hostname"); got != shellPod {
			t.Errorf("hostname = %q, want the service's pod %q", got, shellPod)
		}
	})
	t.Run("by pod name", func(t *testing.T) {
		if got := execOut(shellPod, "--", "hostname"); got != shellPod {
			t.Errorf("hostname = %q, want %q", got, shellPod)
		}
	})
	t.Run("by --pod", func(t *testing.T) {
		if got := execOut("--pod", shellPod, "--", "hostname"); got != shellPod {
			t.Errorf("hostname = %q, want %q", got, shellPod)
		}
	})
	t.Run("project scoping", func(t *testing.T) {
		if got := execOut("--project", project, "shell", "--", "hostname"); got != shellPod {
			t.Errorf("hostname = %q, want %q", got, shellPod)
		}
		_, stderr, code := run("exec", "-n", ns, "--project", "nosuchproject", "shell", "--", "hostname")
		if code == 0 {
			t.Error("a service outside the named project must not resolve")
		}
		if !strings.Contains(stderr, `project "nosuchproject"`) {
			t.Errorf("stderr should name the scope that came up empty, got: %s", stderr)
		}
	})

	// --- streams ---
	t.Run("stdin", func(t *testing.T) {
		cmd := exec.Command(orcinusBin, "exec", "-n", ns, "-i", "shell", "--", "cat")
		cmd.Dir, cmd.Env = repoRoot(), env
		cmd.Stdin = strings.NewReader("piped-payload\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("exec -i: %v", err)
		}
		if strings.TrimSpace(string(out)) != "piped-payload" {
			t.Errorf("stdin round-trip = %q, want piped-payload", out)
		}
	})
	t.Run("stdout and stderr stay separate", func(t *testing.T) {
		stdout, stderr, code := run("exec", "-n", ns, "shell", "--",
			"sh", "-c", "echo to-stdout; echo to-stderr >&2")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if strings.TrimSpace(stdout) != "to-stdout" {
			t.Errorf("stdout = %q, want only to-stdout", stdout)
		}
		if !strings.Contains(stderr, "to-stderr") {
			t.Errorf("stderr = %q, want to-stderr", stderr)
		}
	})

	// --- exit status is the remote command's own ---
	t.Run("exit status", func(t *testing.T) {
		for _, tc := range []struct {
			script string
			want   int
		}{{"exit 0", 0}, {"exit 1", 1}, {"exit 42", 42}} {
			_, stderr, code := run("exec", "-n", ns, "shell", "--", "sh", "-c", tc.script)
			if code != tc.want {
				t.Errorf("`%s` exited %d, want %d", tc.script, code, tc.want)
			}
			// A command that ran and failed is not an orcinus error, so orcinus
			// must not add a message of its own on top of it.
			if strings.Contains(stderr, "Error:") {
				t.Errorf("`%s` printed an orcinus error: %s", tc.script, stderr)
			}
		}
	})

	// --- container selection ---
	t.Run("container selection", func(t *testing.T) {
		if got := execOut("--pod", duoPod, "-c", "app", "--", "sh", "-c", "echo $WHO"); got != "i-am-app" {
			t.Errorf("-c app ran in %q, want i-am-app", got)
		}
		if got := execOut("--pod", duoPod, "-c", "sidecar", "--", "sh", "-c", "echo $WHO"); got != "i-am-sidecar" {
			t.Errorf("-c sidecar ran in %q, want i-am-sidecar", got)
		}
		// No -c: the pod's default-container annotation decides, and because it
		// does, there is nothing to warn about.
		stdout, stderr, code := run("exec", "-n", ns, "--pod", duoPod, "--", "sh", "-c", "echo $WHO")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if strings.TrimSpace(stdout) != "i-am-sidecar" {
			t.Errorf("default container = %q, want the annotated i-am-sidecar", stdout)
		}
		if strings.Contains(stderr, "containers") {
			t.Errorf("an annotated default container needs no warning, got: %s", stderr)
		}
	})

	// --- choices made for you are announced on stderr, never on stdout ---
	t.Run("multiple pods are noted on stderr", func(t *testing.T) {
		if out, err := orcinus("scale", "shell", "2", "-n", ns); err != nil {
			t.Fatalf("scale: %v\n%s", err, out)
		}
		defer func() { _, _ = orcinus("scale", "shell", "1", "-n", ns) }()
		waitFor(t, 90*time.Second, "a second shell pod running", func() bool {
			out, _ := orcinus("kubectl", "get", "pods", "-n", ns,
				"-l", "io.kompose.service=shell", "--field-selector=status.phase=Running", "--no-headers")
			return strings.Count(strings.TrimSpace(out), "\n") >= 1
		})

		stdout, stderr, code := run("exec", "-n", ns, "shell", "--", "hostname")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if !strings.Contains(stderr, "running pods") || !strings.Contains(stderr, "--pod") {
			t.Errorf("stderr should say which pod was picked and how to pin one, got: %s", stderr)
		}
		// The whole point of putting the note on stderr: stdout stays pipeable.
		if lines := strings.Fields(strings.TrimSpace(stdout)); len(lines) != 1 {
			t.Errorf("stdout should hold only the command's output, got: %q", stdout)
		}
	})

	// --- errors ---
	t.Run("errors", func(t *testing.T) {
		for _, tc := range []struct {
			name, want string
			args       []string
		}{
			{"unknown service", `no service or pod "nosuchsvc"`, []string{"nosuchsvc", "--", "sh"}},
			{"unknown container", `no container "nosuchctr"`, []string{"--pod", duoPod, "-c", "nosuchctr", "--", "sh"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stdout, stderr, code := run(append([]string{"exec", "-n", ns}, tc.args...)...)
				if code != 1 {
					t.Errorf("exit %d, want 1 for an orcinus-level failure", code)
				}
				if !strings.Contains(stderr, tc.want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, tc.want)
				}
				if stdout != "" {
					t.Errorf("nothing should reach stdout on failure, got %q", stdout)
				}
			})
		}
	})

	t.Log("live exec e2e passed against the configured cluster")
}

// asExitError is errors.As for *exec.ExitError, kept small so the helper above
// reads as one thing.
func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

func mustOrcinus(t *testing.T, orcinus func(...string) (string, error), args ...string) string {
	t.Helper()
	out, err := orcinus(args...)
	if err != nil {
		t.Fatalf("orcinus %v: %v\n%s", args, err, out)
	}
	return out
}
