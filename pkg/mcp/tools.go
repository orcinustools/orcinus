package mcp

import (
	"context"
	"fmt"

	"github.com/orcinustools/orcinus/pkg/cluster"
	"github.com/orcinustools/orcinus/pkg/deploy"
	"github.com/orcinustools/orcinus/pkg/engine"
	"github.com/orcinustools/orcinus/pkg/plugin"
	"github.com/orcinustools/orcinus/pkg/skills"
	"github.com/orcinustools/orcinus/pkg/version"
)

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func boolp(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func intp(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func nsOrDefault(a map[string]any) string {
	if ns := argStr(a, "namespace"); ns != "" {
		return ns
	}
	return "default"
}

func builtinTools() []tool {
	return []tool{
		{
			Name:        "orcinus_skills",
			Description: "Learn how to use orcinus. Returns a recipe by name, or the catalog if no name.",
			Schema:      obj(map[string]any{"name": str("skill name (omit to list all)")}),
			Handle: func(_ *Server, _ context.Context, a map[string]any) (string, error) {
				if n := argStr(a, "name"); n != "" {
					c, ok := skills.Get(n)
					if !ok {
						return "", fmt.Errorf("unknown skill %q", n)
					}
					return "# " + c.Name + " — " + c.Description + "\n\n" + c.Body, nil
				}
				return skills.All(), nil
			},
		},
		{
			Name:        "version",
			Description: "orcinus build and component versions.",
			Schema:      obj(nil),
			Handle: func(_ *Server, _ context.Context, _ map[string]any) (string, error) {
				return fmt.Sprintf("orcinus %s (commit %s)\nkompose: %s", version.Version, version.GitCommit, version.KomposeRef), nil
			},
		},
		{
			Name:        "convert",
			Description: "Render a docker-compose/manifest source to Kubernetes YAML without applying (dry-run).",
			Schema: obj(map[string]any{
				"source":    str("compose and/or manifest YAML"),
				"project":   str("ownership label (default: default)"),
				"namespace": str("target namespace"),
			}, "source"),
			Handle: func(_ *Server, _ context.Context, a map[string]any) (string, error) {
				req := engine.Request{Project: orDefault(argStr(a, "project"), "default"), Namespace: argStr(a, "namespace")}
				objs, err := engine.BuildObjects([][]byte{[]byte(argStr(a, "source"))}, req)
				if err != nil {
					return "", err
				}
				out, err := deploy.Render(objs)
				return string(out), err
			},
		},
		{
			Name:        "list_projects",
			Description: "List deployed orcinus projects and readiness.",
			Schema:      obj(map[string]any{"namespace": str("limit to a namespace")}),
			Handle: func(s *Server, ctx context.Context, a map[string]any) (string, error) {
				ap, err := s.applier()
				if err != nil {
					return "", err
				}
				p, err := ap.ListProjects(ctx, argStr(a, "namespace"))
				if err != nil {
					return "", err
				}
				return toJSON(p), nil
			},
		},
		{
			Name:        "list_pods",
			Description: "List a project's pods (status, node).",
			Schema:      obj(map[string]any{"project": str("project name"), "namespace": str("namespace")}, "project"),
			Handle: func(s *Server, ctx context.Context, a map[string]any) (string, error) {
				ap, err := s.applier()
				if err != nil {
					return "", err
				}
				pods, err := ap.ListProjectPods(ctx, argStr(a, "project"), argStr(a, "namespace"))
				if err != nil {
					return "", err
				}
				return toJSON(pods), nil
			},
		},
		{
			Name:        "list_nodes",
			Description: "List cluster nodes (status, roles, version).",
			Schema:      obj(nil),
			Handle: func(s *Server, ctx context.Context, _ map[string]any) (string, error) {
				ap, err := s.applier()
				if err != nil {
					return "", err
				}
				n, err := ap.ListNodes(ctx)
				if err != nil {
					return "", err
				}
				return toJSON(n), nil
			},
		},
		{
			Name:        "list_plugins",
			Description: "List available cluster add-on plugins and install state.",
			Schema:      obj(nil),
			Handle: func(_ *Server, _ context.Context, _ map[string]any) (string, error) {
				type pi struct {
					Name, Description, Version string
					Installed                 bool
				}
				var out []pi
				for _, p := range plugin.List() {
					out = append(out, pi{p.Name, p.Description, p.Version, plugin.Installed(p.Name)})
				}
				return toJSON(out), nil
			},
		},
		{
			Name:        "cluster_status",
			Description: "Cluster name, state, runtime and nodes.",
			Schema:      obj(nil),
			Handle: func(_ *Server, _ context.Context, _ map[string]any) (string, error) {
				st, err := cluster.Status("")
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("name=%s running=%t runtime=%s server=%s\n%s",
					st.State.Name, st.Running, st.State.Runtime, st.State.ServerURL, st.Nodes), nil
			},
		},

		// --- mutating tools (require --allow-write) ---
		{
			Name:        "deploy",
			Description: "Convert + apply a compose/manifest source to the cluster.",
			Write:       true,
			Schema: obj(map[string]any{
				"source":    str("compose and/or manifest YAML"),
				"project":   str("ownership label"),
				"namespace": str("target namespace"),
				"wait":      boolp("wait until workloads are ready"),
				"prune":     boolp("remove owned resources no longer present (default true)"),
			}, "source"),
			Handle: func(s *Server, ctx context.Context, a map[string]any) (string, error) {
				prune := true
				if v, ok := a["prune"].(bool); ok {
					prune = v
				}
				req := engine.Request{
					Project: orDefault(argStr(a, "project"), "default"), Namespace: argStr(a, "namespace"),
					Kubeconfig: s.kubeconfig, Prune: prune, Wait: argBool(a, "wait"), AutoInstall: true,
				}
				applied, installed, err := engine.Deploy(ctx, [][]byte{[]byte(argStr(a, "source"))}, req)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("applied %d object(s) as project %q; installed plugins: %v", applied, req.Project, installed), nil
			},
		},
		{
			Name:        "remove_project",
			Description: "Remove all resources of a project.",
			Write:       true,
			Schema:      obj(map[string]any{"project": str("project name"), "namespace": str("namespace")}, "project"),
			Handle: func(s *Server, ctx context.Context, a map[string]any) (string, error) {
				ap, err := s.applier()
				if err != nil {
					return "", err
				}
				n, err := ap.RemoveProject(ctx, argStr(a, "project"), nsOrDefault(a))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("removed %d resource(s)", n), nil
			},
		},
		{
			Name:        "scale",
			Description: "Scale a service to N replicas.",
			Write:       true,
			Schema:      obj(map[string]any{"service": str("service name"), "replicas": intp("replica count"), "namespace": str("namespace")}, "service", "replicas"),
			Handle: func(s *Server, ctx context.Context, a map[string]any) (string, error) {
				n, ok := argInt(a, "replicas")
				if !ok {
					return "", fmt.Errorf("replicas is required")
				}
				ap, err := s.applier()
				if err != nil {
					return "", err
				}
				kind, err := ap.Scale(ctx, nsOrDefault(a), argStr(a, "service"), int32(n))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("scaled %s to %d", kind, n), nil
			},
		},
		{
			Name:        "rollback",
			Description: "Roll a service back to its previous revision.",
			Write:       true,
			Schema:      obj(map[string]any{"service": str("service name"), "namespace": str("namespace")}, "service"),
			Handle: func(s *Server, ctx context.Context, a map[string]any) (string, error) {
				ap, err := s.applier()
				if err != nil {
					return "", err
				}
				return ap.Rollback(ctx, nsOrDefault(a), argStr(a, "service"))
			},
		},
		{
			Name:        "create_registry_secret",
			Description: "Create a private-registry pull secret (verifies the login first).",
			Write:       true,
			Schema: obj(map[string]any{
				"name": str("secret name"), "server": str("registry host"),
				"username": str("username"), "password": str("password/token"), "namespace": str("namespace"),
			}, "name", "server", "username", "password"),
			Handle: func(s *Server, ctx context.Context, a map[string]any) (string, error) {
				if err := deploy.VerifyRegistryLogin(ctx, argStr(a, "server"), argStr(a, "username"), argStr(a, "password"), false); err != nil {
					return "", err
				}
				ap, err := s.applier()
				if err != nil {
					return "", err
				}
				if err := ap.ApplyDockerRegistrySecret(ctx, nsOrDefault(a), argStr(a, "name"), argStr(a, "server"), argStr(a, "username"), argStr(a, "password"), ""); err != nil {
					return "", err
				}
				return "registry secret " + argStr(a, "name") + " created", nil
			},
		},
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
