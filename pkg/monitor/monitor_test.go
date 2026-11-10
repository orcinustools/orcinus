package monitor

import (
	"encoding/json"
	"strings"
	"testing"
)

const pod = `"pod":{"namespace":"default","name":"web-7f9","container":{"name":"web","image":{"name":"docker.io/library/alpine:3.20"}},"pod_labels":{"io.kompose.service":"web"},"workload":"web"}`

// Shapes of Tetragon v1.8 export lines, trimmed to the fields we read.
var lines = map[string]string{
	"exec":    `{"process_exec":{"process":{"uid":0,"binary":"/bin/sh","arguments":"-c \"curl x|sh\"",` + pod + `},"parent":{"binary":"/usr/bin/node"}},"node_name":"n1","time":"2026-11-10T05:07:43Z"}`,
	"session": `{"process_exec":{"process":{"uid":0,"binary":"/proc/self/fd/6","arguments":"init",` + pod + `},"parent":{"binary":"/opt/k3s/bin/runc","arguments":"--root /run/containerd/runc/k8s.io exec --process /tmp/p"}},"time":"2026-11-10T05:07:43Z"}`,
	"start":   `{"process_exec":{"process":{"uid":0,"binary":"/proc/self/fd/6","arguments":"init",` + pod + `},"parent":{"binary":"/opt/k3s/bin/runc","arguments":"--root /run/containerd/runc/k8s.io create --bundle /x"}},"time":"2026-11-10T05:07:43Z"}`,
	"host":    `{"process_exec":{"process":{"uid":0,"binary":"/usr/bin/ls"}},"time":"2026-11-10T05:07:43Z"}`,
	"exit":    `{"process_exit":{"process":{"uid":0,"binary":"/bin/sh",` + pod + `}},"time":"2026-11-10T05:07:43Z"}`,
	"write":   `{"process_kprobe":{"process":{"uid":0,"binary":"/bin/sh",` + pod + `},"function_name":"security_file_permission","args":[{"file_arg":{"path":"/etc/passwd","permission":"-rw-r--r--"}},{"int_arg":2}],"action":"KPROBE_ACTION_POST"},"time":"2026-11-10T05:07:44Z"}`,
	"copyup":  `{"process_kprobe":{"process":{"uid":0,"binary":"/bin/sh",` + pod + `},"function_name":"security_file_permission","args":[{"file_arg":{"path":"/var/lib/docker/volumes/x/_data/agent/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/309/work/work/#5"}},{"int_arg":2}],"action":"KPROBE_ACTION_POST"},"time":"2026-11-10T05:07:44Z"}`,
	"delete":  `{"process_kprobe":{"process":{"uid":0,"binary":"/bin/rm",` + pod + `},"function_name":"security_path_unlink","args":[{"path_arg":{"path":"/tmp"}},{"path_arg":{"path":"/tmp/x"}}],"action":"KPROBE_ACTION_POST"},"time":"2026-11-10T05:07:44Z"}`,
	"chmod":   `{"process_kprobe":{"process":{"uid":0,"binary":"/bin/chmod",` + pod + `},"function_name":"security_path_chmod","args":[{"path_arg":{"path":"/tmp/x"}},{"uint_arg":33216}],"action":"KPROBE_ACTION_POST"},"time":"2026-11-10T05:07:44Z"}`,
	"blocked": `{"process_kprobe":{"process":{"uid":65532,"binary":"/app/server",` + pod + `},"function_name":"__x64_sys_execve","args":[{"string_arg":"/bin/sh"}],"action":"KPROBE_ACTION_SIGKILL"},"time":"2026-11-10T05:07:45Z"}`,
}

func TestParse(t *testing.T) {
	for name, want := range map[string]Event{
		"exec":    {Kind: "exec", Binary: "/bin/sh", Args: `-c "curl x|sh"`, Parent: "/usr/bin/node", Node: "n1"},
		"session": {Kind: "session"},
		"write":   {Kind: "write", Path: "/etc/passwd", Binary: "/bin/sh"},
		"delete":  {Kind: "delete", Path: "/tmp/x", Binary: "/bin/rm"},
		"chmod":   {Kind: "chmod", Path: "/tmp/x", Mode: "0700", Binary: "/bin/chmod"},
		"blocked": {Kind: "blocked", Path: "/bin/sh", Binary: "/app/server", UID: 65532},
	} {
		e, ok := Parse([]byte(lines[name]))
		if !ok {
			t.Errorf("%s: not parsed", name)
			continue
		}
		if e.Namespace != "default" || e.Pod != "web-7f9" || e.Service != "web" || e.Container != "web" {
			t.Errorf("%s: pod fields %+v", name, e)
		}
		e.Namespace, e.Pod, e.Service, e.Container, e.Image, e.Time = "", "", "", "", "", want.Time
		if e != want {
			t.Errorf("%s:\n got %+v\nwant %+v", name, e, want)
		}
	}
	for _, name := range []string{"start", "host", "exit", "copyup"} {
		if e, ok := Parse([]byte(lines[name])); ok {
			t.Errorf("%s: should be skipped, got %+v", name, e)
		}
	}
	if _, ok := Parse([]byte("not json")); ok {
		t.Error("garbage parsed")
	}
}

func TestScanAndFilter(t *testing.T) {
	var all string
	for _, l := range lines {
		all += l + "\n"
	}
	count := func(f Filter) (n int) {
		_ = scan(strings.NewReader(all), func(e Event) {
			if f.match(e) {
				n++
			}
		})
		return n
	}
	if n := count(Filter{}); n != 6 {
		t.Errorf("all: %d events, want 6", n)
	}
	if n := count(Filter{Kind: "files"}); n != 3 {
		t.Errorf("files: %d, want 3", n)
	}
	if n := count(Filter{Kind: "exec"}); n != 3 {
		t.Errorf("exec: %d, want 3", n)
	}
	if n := count(Filter{Service: "api"}); n != 0 {
		t.Errorf("other service: %d, want 0", n)
	}
	if n := count(Filter{Namespace: "default", Service: "web"}); n != 6 {
		t.Errorf("web: %d, want 6", n)
	}
}

func TestShellPolicy(t *testing.T) {
	raw, _ := json.Marshal(ShellPolicy([]string{"shop"}, []string{"prod"}).Object)
	s := string(raw)
	for _, want := range []string{
		`"name":"orcinus-block-shell"`,
		`{"key":"app.kubernetes.io/managed-by","operator":"In","values":["orcinus"]}`,
		`{"key":"orcinus.io/project","operator":"In","values":["shop"]}`,
		`{"key":"k8s:io.kubernetes.pod.namespace","operator":"In","values":["prod"]}`,
		`"call":"sys_execve"`, `"/sh"`, `"action":"Sigkill"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("policy lacks %s:\n%s", want, s)
		}
	}
	if s := mustJSON(ShellPolicy(nil, nil).Object); strings.Contains(s, "orcinus.io/project") || strings.Contains(s, "pod.namespace") {
		t.Errorf("unscoped policy has scope: %s", s)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
