package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/deploy"
	"github.com/orcinustools/orcinus/pkg/engine"
)

type deployOpts struct {
	files      []string
	namespace  string
	project    string
	dryRun     bool
	as         string
	output     string
	replicas   int
	pvcSize    string
	kubeconfig string
	prune      bool
	wait       bool
	acmeEmail  string
}

func newDeployCmd() *cobra.Command {
	o := &deployOpts{}
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Auto-detect compose|manifest and deploy (docs/USAGE.md §5.5)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDeploy(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&o.files, "file", "f", nil, "input file, URL, or '-' for stdin (repeatable). Default: auto-detect orcinus.yml/compose file in the current directory")
	f.StringVarP(&o.namespace, "namespace", "n", "", "target namespace")
	f.StringVar(&o.project, "project", "", "ownership label (default: current directory name)")
	f.BoolVar(&o.dryRun, "dry-run", false, "render instead of applying")
	f.StringVar(&o.as, "as", "", "force input mode: compose|manifest (default: auto-detect)")
	f.StringVarP(&o.output, "output", "o", "", "also write converted manifests to this directory")
	f.IntVar(&o.replicas, "replicas", 1, "default replicas when a service specifies none")
	f.StringVar(&o.pvcSize, "pvc-size", "1Gi", "default PersistentVolumeClaim size")
	f.StringVar(&o.kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	f.BoolVar(&o.prune, "prune", true, "remove owned resources no longer present in the input")
	f.BoolVar(&o.wait, "wait", false, "wait until workloads are ready")
	f.StringVar(&o.acmeEmail, "acme-email", "", "email for auto-installing cert-manager when x-orcinus-tls is used")
	return cmd
}

func runDeploy(cmd *cobra.Command, o *deployOpts) error {
	if o.project == "" {
		if wd, err := os.Getwd(); err == nil {
			o.project = filepath.Base(wd)
		}
	}

	// No -f: discover a default project file in the current directory,
	// preferring orcinus.yml (docs/USAGE.md §3.5).
	if len(o.files) == 0 {
		found, err := discoverDefaultFile()
		if err != nil {
			return err
		}
		o.files = []string{found}
		fmt.Fprintf(cmd.ErrOrStderr(), "using %s\n", found)
	}

	// Read every source (file, URL, or stdin) into raw bytes.
	var sources [][]byte
	for _, src := range o.files {
		raw, err := readSource(src, cmd.InOrStdin())
		if err != nil {
			return err
		}
		sources = append(sources, raw)
	}

	req := engine.Request{
		Project:     o.project,
		Namespace:   o.namespace,
		Mode:        o.as,
		Replicas:    o.replicas,
		PVCSize:     o.pvcSize,
		Kubeconfig:  o.kubeconfig,
		Prune:       o.prune,
		Wait:        o.wait,
		ACMEEmail:   o.acmeEmail,
		AutoInstall: true,
	}

	// detect + convert (compose) / passthrough (manifest).
	objects, err := engine.BuildObjects(sources, req)
	if err != nil {
		return err
	}

	if o.output != "" {
		if err := deploy.WriteDir(objects, o.output); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d object(s) to %s\n", len(objects), o.output)
	}
	if o.dryRun {
		out, err := deploy.Render(objects)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(out)
		return err
	}

	// Auto-install cert-manager / argo-rollouts if the input requires them.
	if engine.NeedsCertManager(objects) && o.acmeEmail == "" {
		return fmt.Errorf("x-orcinus-tls needs cert-manager; run `orcinus plugin install cert-manager --email you@example.com` or pass --acme-email")
	}
	installed, err := engine.AutoInstall(cmd.Context(), objects, req)
	if err != nil {
		return err
	}
	for _, p := range installed {
		fmt.Fprintf(cmd.ErrOrStderr(), "installed %s (required by the input)\n", p)
	}

	applied, err := engine.Apply(cmd.Context(), objects, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "applied %d object(s) as project %q\n", applied, o.project)
	return nil
}

// defaultFiles lists, in priority order, the files `orcinus deploy` looks for
// when no -f is given. orcinus.yml wins over compose files.
var defaultFiles = []string{
	"orcinus.yml", "orcinus.yaml",
	"compose.yaml", "compose.yml",
	"docker-compose.yml", "docker-compose.yaml",
}

func discoverDefaultFile() (string, error) {
	for _, name := range defaultFiles {
		if fi, err := os.Stat(name); err == nil && !fi.IsDir() {
			return name, nil
		}
	}
	return "", fmt.Errorf("no input file found (looked for %s); pass -f explicitly",
		strings.Join(defaultFiles, ", "))
}

func readSource(src string, stdin io.Reader) ([]byte, error) {
	switch {
	case src == "-":
		return io.ReadAll(stdin)
	case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "https://"):
		return readURL(src)
	default:
		b, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", src, err)
		}
		return b, nil
	}
}

// readURL fetches a manifest/compose file over HTTP(S), like `kubectl apply -f <url>`.
func readURL(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %q: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %q: HTTP %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

